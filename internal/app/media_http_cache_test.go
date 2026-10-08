package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPPrivatePreviewCacheRevalidatesOwnerAndDeletedSources(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookie)
	owner, _ := ownerFromRequest(request)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "preview")
	if err := os.WriteFile(path, pngData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	v := Source{ID: newID("src"), Owner: owner, Title: "Preview", Kind: "upload", DurationMS: 10000, Path: path, ThumbnailPath: path}
	if err := s.AddSource(ctx, v); err != nil {
		t.Fatal(err)
	}
	other := make([]byte, 32)
	other[0] = 1
	foreign := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(other)}
	handler := NewServer(Config{}, s).Handler()
	for _, suffix := range []string{"/media", "/thumbnail"} {
		t.Run(suffix, func(t *testing.T) {
			url := "/api/v1/sources/" + v.ID + suffix
			get := func(cookie *http.Cookie, tag string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", url, nil)
				if cookie != nil {
					r.AddCookie(cookie)
				}
				if tag != "" {
					r.Header.Set("If-None-Match", tag)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			first := get(cookie, "")
			etag := first.Header().Get("ETag")
			if first.Code != 200 || etag == "" || first.Body.Len() == 0 || first.Header().Get("Cache-Control") != "private, max-age=0, must-revalidate" || !strings.Contains(first.Header().Get("Vary"), "Cookie") {
				t.Fatal("authorized preview did not receive private revalidation headers")
			}
			hit := get(cookie, etag)
			if hit.Code != http.StatusNotModified || hit.Body.Len() != 0 {
				t.Fatal("unchanged preview was transferred again")
			}
			for _, tt := range []struct {
				cookie *http.Cookie
				status int
			}{{nil, 401}, {foreign, 404}} {
				w := get(tt.cookie, etag)
				if w.Code != tt.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
					t.Fatal("a preview cache bypassed session ownership")
				}
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if w := get(cookie, etag); w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing physical preview remained cacheable")
			}
			if err := os.WriteFile(path, pngData.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := s.DB.Exec(ctx, "DELETE FROM sources WHERE id=$1", v.ID); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/media", "/thumbnail"} {
		r := httptest.NewRequest("GET", "/api/v1/sources/"+v.ID+suffix, nil)
		r.AddCookie(cookie)
		r.Header.Set("If-None-Match", "*")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("deleted source remained available through conditional caching")
		}
	}
}

func TestHTTPPreviewCachePreservesRangesAndDoesNotCacheDownloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview.mp4")
	content := bytes.Repeat([]byte("v"), 256<<10)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	serveFile(first, httptest.NewRequest("GET", "/", nil), path, "source.mp4", false)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Range", "bytes=1024-2047")
	r.Header.Set("If-Range", first.Header().Get("ETag"))
	ranged := httptest.NewRecorder()
	serveFile(ranged, r, path, "source.mp4", false)
	if ranged.Code != http.StatusPartialContent || ranged.Body.Len() != 1024 || ranged.Header().Get("Content-Range") != "bytes 1024-2047/262144" {
		t.Fatal("conditional source caching broke media range requests")
	}
	download := httptest.NewRecorder()
	download.Header().Set("Cache-Control", "no-store")
	serveFile(download, httptest.NewRequest("GET", "/", nil), path, "result.mp4", true)
	if download.Code != http.StatusOK || download.Header().Get("Cache-Control") != "no-store" || download.Header().Get("ETag") != "" {
		t.Fatal("an export download received source-preview caching")
	}
}
