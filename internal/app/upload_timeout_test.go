package app

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
