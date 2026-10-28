package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type store struct {
	endpoint, access, secret string
	client                   *http.Client
}
type storageError struct {
	Code   string `xml:"Code"`
	Status int
}

func (e *storageError) Error() string { return fmt.Sprintf("S3 %s (HTTP %d)", e.Code, e.Status) }
func sum(b []byte) string             { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}
func newStore() *store {
	return &store{strings.TrimRight(os.Getenv("S3_ENDPOINT"), "/"), os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *store) request(ctx context.Context, method, bucket, key string, q url.Values, body io.Reader, size int64, hash string) (*http.Response, error) {
	u, err := url.Parse(s.endpoint)
	if err != nil {
		return nil, fmt.Errorf("storage endpoint configuration")
	}
	u.Path = "/" + bucket + "/" + key
	if key == "" {
		u.Path = "/" + bucket
	}
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	r, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("storage request configuration")
	}
	r.ContentLength = size
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	r.Header.Set("x-amz-date", stamp)
	r.Header.Set("x-amz-content-sha256", hash)
	headers := "host:" + u.Host + "\nx-amz-content-sha256:" + hash + "\nx-amz-date:" + stamp + "\n"
	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := method + "\n" + u.EscapedPath() + "\n" + u.RawQuery + "\n" + headers + "\n" + signed + "\n" + hash
	scope := day + "/us-east-1/s3/aws4_request"
	signing := mac(mac(mac(mac([]byte("AWS4"+s.secret), day), "us-east-1"), "s3"), "aws4_request")
	signature := hex.EncodeToString(mac(signing, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+sum([]byte(canonical))))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.access+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
	resp, err := s.client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("storage connection unavailable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		e := &storageError{Status: resp.StatusCode}
		xml.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(e)
		if e.Code == "" {
			e.Code = "RequestFailed"
		}
		return nil, e
	}
	return resp, nil
}
func (s *store) absent(ctx context.Context, bucket, key string) error {
	// Exact-key prefix query; never list an entire bucket or historical data.
	q := url.Values{"list-type": {"2"}, "prefix": {key}, "max-keys": {"1"}}
	r, e := s.request(ctx, "GET", bucket, "", q, nil, 0, sum(nil))
	if e != nil {
		return e
	}
	defer r.Body.Close()
	var listing struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if e = xml.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&listing); e != nil {
		return fmt.Errorf("storage existence check failed")
	}
	for _, x := range listing.Contents {
		if x.Key == key {
			return fmt.Errorf("destination already exists")
		}
	}
	return nil
}
func (s *store) putFile(ctx context.Context, bucket, key, path, hash string, fresh bool) error {
	if fresh {
		if e := s.absent(ctx, bucket, key); e != nil {
			return e
		}
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	r, e := s.request(ctx, "PUT", bucket, key, nil, f, st.Size(), hash)
	if e == nil {
		r.Body.Close()
	}
	return e
}
func (s *store) get(ctx context.Context, bucket, key string) (*http.Response, error) {
	return s.request(ctx, "GET", bucket, key, nil, nil, 0, sum(nil))
}
