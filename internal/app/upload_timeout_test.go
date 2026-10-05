package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUploadTimeoutCoversReadingTheFile(t *testing.T) {
	s := testStore(t)
	c := Config{DataDir: t.TempDir(), SourceTimeout: 200 * time.Millisecond,
		MaxSourceBytes: 1 << 20, MaxOwnerBytes: 2 << 20, MaxStorageBytes: 10 << 20, MutationsPerMinute: 20}
	server := httptest.NewServer(NewServer(c, s).Handler())
	defer server.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	cookie := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	// Complete the file headers, then stall while the server is copying its body.
	_, err = fmt.Fprintf(conn, "POST /api/v1/uploads HTTP/1.1\r\nHost: %s\r\nCookie: cutmy_session=%s\r\nContent-Type: multipart/form-data; boundary=upload\r\nContent-Length: 1000000\r\n\r\n--upload\r\nContent-Disposition: form-data; name=\"file\"; filename=\"slow.mp4\"\r\nContent-Type: video/mp4\r\n\r\npartial", strings.TrimPrefix(server.URL, "http://"), cookie)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal("upload did not respond within the source timeout", err)
	}
	defer response.Body.Close()
	var body struct{ Error APIError }
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusGatewayTimeout || body.Error.Code != "source_timeout" {
		t.Fatalf("got %d %+v, expected source_timeout", response.StatusCode, body.Error)
	}
	entries, err := os.ReadDir(filepath.Join(c.DataDir, "sources"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("timed-out upload left staged media: %v %v", entries, err)
	}
}

func TestRejectedSmallUploadDoesNotDrainStalledBody(t *testing.T) {
	s := testStore(t)
	c := Config{DataDir: t.TempDir(), SourceTimeout: 200 * time.Millisecond,
		MaxSourceBytes: 1 << 20, MaxOwnerBytes: 2 << 20, MaxStorageBytes: 10 << 20, MutationsPerMinute: 20}
	server := httptest.NewServer(NewServer(c, s).Handler())
	defer server.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	cookie := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	// Bodies below net/http's automatic drain threshold previously outlived
	// the handler's timeout when an invalid first part rejected them early.
	_, err = fmt.Fprintf(conn, "POST /api/v1/uploads HTTP/1.1\r\nHost: %s\r\nCookie: cutmy_session=%s\r\nContent-Type: multipart/form-data; boundary=upload\r\nContent-Length: 10000\r\n\r\n--upload\r\nContent-Disposition: form-data; name=\"wrong-field\"; filename=\"slow.mp4\"\r\nContent-Type: video/mp4\r\n\r\npartial", strings.TrimPrefix(server.URL, "http://"), cookie)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal("rejected upload kept waiting for untrusted body bytes", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("rejected upload returned %d", response.StatusCode)
	}
	if !response.Close {
		t.Fatal("rejected unread upload left its connection reusable")
	}
}

func TestCompletedUploadPreservesConnectionContextAndPreview(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.SourceTimeout = 3 * time.Second
	c.MaxOwnerBytes = 2 * c.MaxSourceBytes
	c.MaxStorageBytes = 5 * c.MaxSourceBytes
	c.MutationsPerMinute = 20
	fixture := filepath.Join(t.TempDir(), "fixture.mp4")
	if _, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-t", "1", "-c:v", "libx264", "-threads", "1", fixture); err != nil {
		t.Fatal(err)
	}
	media, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "fixture.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(media); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(c, s).Handler()
	completed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if r.Method == http.MethodPost {
			select {
			case <-r.Context().Done():
				completed <- r.Context().Err()
			case <-time.After(10 * time.Millisecond):
				completed <- nil
			}
		}
	}))
	defer server.Close()
	client := server.Client()
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/uploads", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.AddCookie(cookie)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusCreated {
		t.Fatalf("upload returned %d: %s (%v)", response.StatusCode, data, err)
	}
	if err := <-completed; err != nil {
		t.Fatalf("completed upload cancelled its connection context: %v", err)
	}
	var source Source
	if err := json.Unmarshal(data, &source); err != nil || source.PreviewURL == nil {
		t.Fatalf("upload returned no preview: %s (%v)", data, err)
	}
	request, err = http.NewRequest(http.MethodGet, server.URL+*source.PreviewURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	request.Header.Set("Range", "bytes=0-255")
	reused := false
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if !reused || response.StatusCode != http.StatusPartialContent {
		t.Fatalf("preview on reused connection returned %d; reused=%v", response.StatusCode, reused)
	}
}
