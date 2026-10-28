package main

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"testing"
)

func testApp(t *testing.T) *app {
	t.Helper()
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid request reached storage")
		http.Error(w, "unexpected", 500)
	}))
	t.Cleanup(storage.Close)
	return &app{jobs: map[string]*job{}, queue: make(chan string, 2), uploads: make(chan struct{}, 1), work: t.TempDir(), s3: &store{endpoint: storage.URL, access: "synthetic", secret: "synthetic", client: storage.Client()}}
}
func TestHealthAndUnknownJob(t *testing.T) {
	a := testApp(t)
	for _, tc := range []struct {
		path string
		code int
	}{{"/healthz", 200}, {"/api/jobs/unknown", 404}, {"/api/jobs/unknown/download/360", 404}} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s status %d", tc.path, w.Code)
		}
	}
}
func TestRejectInvalidUploadWithoutStorageWrites(t *testing.T) {
	cases := []struct {
		name, kind string
		data       []byte
	}{{"bad.txt", "text/plain", []byte("invalid")}, {"bad.mp4", "text/plain", []byte("invalid")}, {"empty.mp4", "video/mp4", nil}, {"corrupt.mp4", "video/mp4", []byte("synthetic invalid MP4")}}
	for _, tc := range cases {
		t.Run(tc.name+tc.kind, func(t *testing.T) {
			a := testApp(t)
			var b bytes.Buffer
			m := multipart.NewWriter(&b)
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", `form-data; name="video"; filename="`+tc.name+`"`)
			header.Set("Content-Type", tc.kind)
			part, _ := m.CreatePart(header)
			part.Write(tc.data)
			m.Close()
			req := httptest.NewRequest("POST", "/api/jobs", &b)
			req.Header.Set("Content-Type", m.FormDataContentType())
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, req)
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("invalid upload status %d: %s", w.Code, w.Body.String())
			}
			if len(a.jobs) != 0 {
				t.Fatal("invalid file created job")
			}
			files, e := os.ReadDir(a.work)
			if e != nil || len(files) != 0 {
				t.Fatal("invalid upload left temporary files")
			}
		})
	}
}
func TestRejectCrossOriginUpload(t *testing.T) {
	a := testApp(t)
	req := httptest.NewRequest("POST", "http://lab.local/api/jobs", nil)
	req.Header.Set("Origin", "https://other.local")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestDownloadChecksCompletedJob(t *testing.T) {
	a := testApp(t)
	id := "0123456789abcdef0123456789abcdef"
	a.jobs[id] = &job{ID: id, Results: []result{{Quality: 360, Status: "processing"}}}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/jobs/"+id+"/download/360", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestStorageErrorsNeverExposeResponseBodyOrTransportURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>synthetic-sensitive-marker</Message></Error>`))
	}))
	defer server.Close()
	s := &store{endpoint: server.URL, access: "synthetic", secret: "synthetic", client: server.Client()}
	_, err := s.get(context.Background(), "bucket", "key")
	if err == nil || err.Error() != "S3 AccessDenied (HTTP 403)" {
		t.Fatalf("unexpected sanitized error: %v", err)
	}
}
func TestFrontendContainsNoCredentials(t *testing.T) {
	b, e := assets.ReadFile("index.html")
	if e != nil {
		t.Fatal(e)
	}
	for _, needle := range []string{"S3_SECRET_KEY", "MC_HOST_", "Authorization:", "MINIO_ROOT_PASSWORD"} {
		if bytes.Contains(b, []byte(needle)) {
			t.Fatalf("frontend exposes %s", needle)
		}
	}
	a := testApp(t)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/jobs", nil))
	var jobs []job
	if json.Unmarshal(w.Body.Bytes(), &jobs) != nil || len(jobs) != 0 {
		t.Fatal("empty jobs response")
	}
}
