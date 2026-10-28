package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var assets embed.FS

const maxUpload int64 = 100 << 20

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var qualities = []int{360, 720, 1080}

type media struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Duration float64 `json:"duration"`
	Video    string  `json:"videoCodec"`
	Audio    string  `json:"audioCodec"`
}
type result struct {
	Quality int    `json:"quality"`
	Status  string `json:"status"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
	Media   *media `json:"media,omitempty"`
	Error   string `json:"error,omitempty"`
	Key     string `json:"-"`
}
type job struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Created      string   `json:"created"`
	Source       media    `json:"source"`
	OriginalSHA  string   `json:"originalSha256"`
	OriginalSize int64    `json:"originalSize"`
	Results      []result `json:"results"`
	Error        string   `json:"error,omitempty"`
	Dir          string   `json:"-"`
	Input        string   `json:"-"`
	OriginalKey  string   `json:"-"`
}
type app struct {
	mu      sync.RWMutex
	jobs    map[string]*job
	queue   chan string
	uploads chan struct{}
	s3      *store
	work    string
}

func randomID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b)
}
func fileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func probe(ctx context.Context, path string) (media, error) {
	var doc struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Side      []struct {
				Rotation int `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	out, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
	if e != nil {
		return media{}, errors.New("El archivo no contiene un vídeo válido")
	}
	if json.Unmarshal(out, &doc) != nil {
		return media{}, errors.New("No se pudo inspeccionar el vídeo")
	}
	m := media{}
	for _, s := range doc.Streams {
		if s.CodecType == "video" && m.Width == 0 {
			m.Width = s.Width
			m.Height = s.Height
			m.Video = s.CodecName
			for _, side := range s.Side {
				if side.Rotation%180 != 0 {
					m.Width, m.Height = m.Height, m.Width
				}
			}
		}
		if s.CodecType == "audio" {
			m.Audio = s.CodecName
		}
	}
	m.Duration, _ = strconv.ParseFloat(doc.Format.Duration, 64)
	if m.Width <= 0 || m.Height <= 0 || m.Duration <= 0 {
		return m, errors.New("No se detectó vídeo con duración válida")
	}
	return m, nil
}
func respond(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, code int, s string) {
	respond(w, code, map[string]string{"error": s})
}
func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		b, _ := assets.ReadFile("index.html")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/jobs", a.list)
	mux.HandleFunc("POST /api/jobs", a.upload)
	mux.HandleFunc("GET /api/jobs/{id}", a.getJob)
	mux.HandleFunc("GET /api/jobs/{id}/download/{quality}", a.download)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Method == "POST" {
			if o := r.Header.Get("Origin"); o != "" {
				u, e := url.Parse(o)
				if e != nil || u.Host != r.Host {
					problem(w, 403, "Origen de solicitud no permitido")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *app) list(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	items := []*job{}
	for _, j := range a.jobs {
		items = append(items, j)
	}
	respond(w, 200, items)
}
func (a *app) getJob(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	j := a.jobs[r.PathValue("id")]
	if j == nil {
		problem(w, 404, "Trabajo no encontrado en esta sesión del laboratorio")
		return
	}
	respond(w, 200, j)
}
func (a *app) upload(w http.ResponseWriter, r *http.Request) {
	select {
	case a.uploads <- struct{}{}:
		defer func() { <-a.uploads }()
	default:
		problem(w, 429, "Hay otra subida en curso. Inténtalo de nuevo en unos segundos")
		return
	}
	if len(a.queue) >= cap(a.queue) {
		problem(w, 429, "La cola está llena. Espera a que termine un vídeo")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(1<<20))
	mr, e := r.MultipartReader()
	if e != nil {
		problem(w, 400, "Selecciona un archivo de vídeo")
		return
	}
	part, e := mr.NextPart()
	if e != nil || part.FormName() != "video" {
		problem(w, 400, "Falta el campo video")
		return
	}
	defer part.Close()
	ext := strings.ToLower(filepath.Ext(part.FileName()))
	ct, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
	allowed := map[string]string{".mp4": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm", ".mkv": "video/x-matroska"}
	if allowed[ext] == "" || allowed[ext] != ct {
		problem(w, 400, "Formato no admitido: usa MP4, MOV, WebM o MKV con su tipo de contenido correcto")
		return
	}
	id := randomID()
	dir, e := os.MkdirTemp(a.work, "job-"+id+"-")
	if e != nil {
		problem(w, 507, "No hay espacio temporal disponible")
		return
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	input := filepath.Join(dir, "source"+ext)
	f, e := os.OpenFile(input, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		problem(w, 500, "No se pudo recibir el archivo")
		return
	}
	size, e := io.Copy(f, io.LimitReader(part, maxUpload+1))
	closeErr := f.Close()
	if e != nil || closeErr != nil || size > maxUpload || size == 0 {
		problem(w, 413, "El archivo debe pesar entre 1 byte y 100 MiB")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	m, e := probe(ctx, input)
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	if m.Duration > 60 || m.Width*m.Height > 3840*2160 || m.Height < 360 {
		problem(w, 400, "Demo limitada a 60 segundos, 4K y altura mínima de 360 píxeles")
		return
	}
	hash, e := fileHash(input)
	if e != nil {
		problem(w, 500, "No se pudo comprobar la integridad")
		return
	}
	key := "video-lab/" + id + "/source" + ext
	if e = a.s3.putFile(ctx, "mansiflix-originals", key, input, hash, true); e != nil {
		log.Printf("upload original job=%s failed: %v", id, e)
		problem(w, 503, "El almacenamiento no permite guardar el original; revisa los permisos de Video Lab")
		return
	}
	// Verify the stored original, rather than trusting only PutObject success.
	if e = a.verify(ctx, "mansiflix-originals", key, hash, size); e != nil {
		problem(w, 503, "No se pudo verificar el original guardado")
		return
	}
	j := &job{ID: id, Name: filepath.Base(part.FileName()), Status: "queued", Created: time.Now().UTC().Format(time.RFC3339), Source: m, OriginalSHA: hash, OriginalSize: size, Dir: dir, Input: input, OriginalKey: key}
	for _, q := range qualities {
		state := "queued"
		msg := ""
		if q > m.Height {
			state = "unavailable"
			msg = "La fuente tiene menor resolución; no se amplía"
		}
		j.Results = append(j.Results, result{Quality: q, Status: state, Error: msg, Key: "runs/video-lab/" + id + "/" + strconv.Itoa(q) + ".mp4"})
	}
	a.mu.Lock()
	a.jobs[id] = j
	a.mu.Unlock()
	keep = true
	a.queue <- id
	a.mu.RLock()
	defer a.mu.RUnlock()
	respond(w, 202, j)
}
func (a *app) verify(ctx context.Context, bucket, key, hash string, size int64) error {
	r, e := a.s3.get(ctx, bucket, key)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(r.Body, size+1))
	if e != nil || n != size || hex.EncodeToString(h.Sum(nil)) != hash {
		return errors.New("stored object integrity mismatch")
	}
	return nil
}
func (a *app) workLoop() {
	for id := range a.queue {
		a.process(id)
	}
}
func (a *app) process(id string) {
	a.mu.Lock()
	j := a.jobs[id]
	j.Status = "processing"
	a.mu.Unlock()
	defer os.RemoveAll(j.Dir)
	for i, q := range qualities {
		if q > j.Source.Height {
			continue
		}
		a.mu.Lock()
		j.Results[i].Status = "processing"
		a.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		path := filepath.Join(j.Dir, strconv.Itoa(q)+".mp4")
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-threads", "1", "-i", j.Input, "-map", "0:v:0", "-map", "0:a:0?", "-vf", fmt.Sprintf("scale=-2:%d", q), "-filter_threads", "1", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-crf", "23", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", path}
		e := exec.CommandContext(ctx, "ffmpeg", args...).Run()
		var m media
		var size int64
		var hash string
		if e == nil {
			m, e = probe(ctx, path)
		}
		if e == nil && (m.Height != q || m.Video != "h264" || (j.Source.Audio != "" && m.Audio != "aac") || m.Duration < j.Source.Duration-0.5 || m.Duration > j.Source.Duration+0.5) {
			e = errors.New("encoded media validation failed")
		}
		if e == nil {
			var info os.FileInfo
			info, e = os.Stat(path)
			if e == nil {
				size = info.Size()
				if size == 0 {
					e = errors.New("empty output")
				}
			}
		}
		if e == nil {
			hash, e = fileHash(path)
		}
		key := j.Results[i].Key
		if e == nil {
			e = a.s3.putFile(ctx, "mansiflix-encoded", key, path, hash, true)
		}
		if e == nil {
			e = a.verify(ctx, "mansiflix-encoded", key, hash, size)
		}
		cancel()
		a.mu.Lock()
		if e != nil {
			j.Results[i].Status = "failed"
			j.Results[i].Error = "Falló la codificación o la verificación en almacenamiento"
			j.Error = "Una o más versiones no pudieron completarse"
			log.Printf("process job=%s quality=%d failed: %v", id, q, e)
		} else {
			j.Results[i].Status = "completed"
			j.Results[i].Size = size
			j.Results[i].SHA256 = hash
			j.Results[i].Media = &m
			log.Printf("verified job=%s quality=%d dimensions=%dx%d bytes=%d sha256=%s", id, q, m.Width, m.Height, size, hash)
		}
		a.mu.Unlock()
		os.Remove(path)
	}
	a.mu.Lock()
	j.Status = "completed"
	for _, r := range j.Results {
		if r.Status == "failed" {
			j.Status = "failed"
		}
	}
	a.mu.Unlock()
}
func (a *app) download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q, _ := strconv.Atoi(r.PathValue("quality"))
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	a.mu.RLock()
	j := a.jobs[id]
	var item result
	if j != nil {
		for _, x := range j.Results {
			if x.Quality == q {
				item = x
			}
		}
	}
	a.mu.RUnlock()
	if item.Status != "completed" {
		problem(w, 404, "La versión todavía no está disponible")
		return
	}
	resp, e := a.s3.get(r.Context(), "mansiflix-encoded", item.Key)
	if e != nil {
		problem(w, 503, "No se pudo recuperar la versión guardada")
		return
	}
	defer resp.Body.Close()
	if resp.ContentLength != item.Size {
		problem(w, 502, "El tamaño del resultado guardado no coincide")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="mansiflix-%s-%dp.mp4"`, id, q))
	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))
	w.Header().Set("X-Content-SHA256", item.SHA256)
	io.Copy(w, resp.Body)
}
func main() {
	if os.Getenv("S3_ENDPOINT") == "" || os.Getenv("S3_ACCESS_KEY") == "" || os.Getenv("S3_SECRET_KEY") == "" {
		log.Fatal("Storage configuration is required")
	}
	work := os.Getenv("WORK_DIR")
	if work == "" {
		work = "/work"
	}
	if e := os.MkdirAll(work, 0700); e != nil {
		log.Fatal("Work directory unavailable")
	}
	a := &app{jobs: map[string]*job{}, queue: make(chan string, 2), uploads: make(chan struct{}, 1), s3: newStore(), work: work}
	go a.workLoop()
	server := &http.Server{Addr: ":8080", Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 180 * time.Second, IdleTimeout: 60 * time.Second}
	log.Print("Video Lab listening on :8080; sequential encoder; max upload 100MiB / 60s")
	log.Fatal(server.ListenAndServe())
}
