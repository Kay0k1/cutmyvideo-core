package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type previewBenchmarkWriter struct {
	header http.Header
	status int
	bytes  int64
}

func (w *previewBenchmarkWriter) Header() http.Header    { return w.header }
func (w *previewBenchmarkWriter) WriteHeader(status int) { w.status = status }
func (w *previewBenchmarkWriter) Write(data []byte) (int, error) {
	w.bytes += int64(len(data))
	return len(data), nil
}

func BenchmarkPrivatePreviewRepeat(b *testing.B) {
	path := filepath.Join(b.TempDir(), "preview.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte("v"), 256<<10), 0600); err != nil {
		b.Fatal(err)
	}
	first := httptest.NewRecorder()
	serveFile(first, httptest.NewRequest("GET", "/", nil), path, "source.mp4", false)
	for _, tt := range []struct {
		name, etag string
		status     int
	}{{"uncached_full_response", "", 200}, {"private_revalidation", first.Header().Get("ETag"), 304}} {
		b.Run(tt.name, func(b *testing.B) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.etag != "" {
				r.Header.Set("If-None-Match", tt.etag)
			}
			w := &previewBenchmarkWriter{header: make(http.Header)}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				serveFile(w, r, path, "source.mp4", false)
				if w.status != tt.status {
					b.Fatal("unexpected preview response status")
				}
			}
			b.ReportMetric(float64(w.bytes)/float64(b.N), "body-B/op")
		})
	}
}
