package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
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

func TestHTTPOriginAndCapabilityCookie(t *testing.T) {
	s := NewServer(Config{PublicOrigin: "https://example.com"}, nil)
	for _, origin := range []string{"https://evil.example", "null", "http://example.com"} {
		r := httptest.NewRequest("POST", "https://example.com/api/v1/jobs", nil)
		r.Header.Set("Origin", origin)
		if s.validOrigin(r) {
			t.Errorf("accepted %s", origin)
		}
	}
	r := httptest.NewRequest("POST", "https://example.com/api/v1/jobs", nil)
	r.Header.Set("Origin", "https://example.com")
	if !s.validOrigin(r) {
		t.Fatal("blocked own origin")
	}
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if s.validOrigin(r) {
		t.Fatal("accepted cross-site request")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	owner, ok := ownerFromRequest(r)
	if !ok || len(owner) != 64 {
		t.Fatal("invalid ownership hash")
	}
	other := httptest.NewRequest("GET", "/", nil)
	other.AddCookie(&http.Cookie{Name: "cutmy_session", Value: "not-a-session"})
	if _, ok = ownerFromRequest(other); ok {
		t.Fatal("accepted malformed cookie")
	}
}

func TestRateCapacityKeepsExistingClientsAndBoundsNewKeys(t *testing.T) {
	s := NewServer(Config{}, nil)
	for i := 0; i < maxRateEntries; i++ {
		if !s.allow(fmt.Sprintf("fixture:%d", i), 2) {
			t.Fatal("rate table filled before its advertised capacity")
		}
	}
	if s.allow("overflow", 2) || len(s.rate) != maxRateEntries {
		t.Fatal("unbounded rate table")
	}
	if !s.allow("fixture:0", 2) || s.allow("fixture:0", 2) {
		t.Fatal("a full table changed an existing client's allowance")
	}
	s.rate["fixture:1"].reset = time.Now().Add(-time.Second)
	s.rateCleanup = time.Now().Add(-time.Second)
	if !s.allow("new-client", 1) || len(s.rate) != maxRateEntries {
		t.Fatal("expired entries were not reclaimed")
	}
	if s.rateCleanup.Before(time.Now()) {
		t.Fatal("cleanup will repeat on every blocked request")
	}
}

func TestRotatingCookiesCannotFillRateTableFromRejectedMutations(t *testing.T) {
	s := NewServer(Config{MutationsPerMinute: 1}, nil)
	accepted := 0
	handler := s.withSession(func(w http.ResponseWriter, _ *http.Request, _ string) {
		accepted++
		w.WriteHeader(http.StatusNoContent)
	})
	for i := 0; i < 200; i++ {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		cookie := make([]byte, 32)
		cookie[0] = byte(i)
		r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(cookie)})
		handler(httptest.NewRecorder(), r)
	}
	if accepted != 1 || len(s.rate) != 3 {
		t.Fatalf("rejected mutations allocated owner state: accepted=%d entries=%d", accepted, len(s.rate))
	}
}

func TestRotatingCookiesCannotBypassIPReadLimit(t *testing.T) {
	s := NewServer(Config{}, nil)
	accepted := 0
	handler := s.withSession(func(w http.ResponseWriter, _ *http.Request, _ string) {
		accepted++
		w.WriteHeader(http.StatusNoContent)
	})
	for i := 0; i < requestsPerIPPerMinute+10; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		cookie := make([]byte, 32)
		cookie[0], cookie[1] = byte(i), byte(i>>8)
		r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(cookie)})
		handler(httptest.NewRecorder(), r)
	}
	if accepted != requestsPerIPPerMinute || len(s.rate) != requestsPerIPPerMinute+1 {
		t.Fatalf("rotated read cookies bypassed IP admission: accepted=%d entries=%d", accepted, len(s.rate))
	}
}

func TestJSONBodyReadUsesEarlierDeadline(t *testing.T) {
	for _, partial := range []string{`{"url":"unfinished`, `{"url":"complete"}`} {
		t.Run(partial, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
				defer cancel()
				var body struct {
					URL string `json:"url"`
				}
				if decodeJSON(w, r.WithContext(ctx), &body) == nil {
					t.Error("accepted unfinished request body")
				}
			}))
			defer server.Close()
			conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: fixture\r\nContent-Length: 10000\r\n\r\n%s", partial); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatal("stalled JSON body did not release its connection", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusRequestTimeout {
				t.Fatalf("stalled JSON returned %d", response.StatusCode)
			}
		})
	}
}

func TestEarlyRejectionDoesNotDrainUntrustedBody(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, origin string
		status                     int
	}{
		{"missing session", http.MethodPost, "/api/v1/uploads", "", http.StatusUnauthorized},
		{"cross-site", http.MethodPost, "/api/v1/jobs", "https://attacker.invalid", http.StatusForbidden},
		{"unexpected GET body", http.MethodGet, "/healthz", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(NewServer(Config{PublicOrigin: "https://example.com"}, nil).Handler())
			defer server.Close()
			conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: fixture\r\nOrigin: %s\r\nContent-Length: 10000\r\n\r\npartial", tc.method, tc.path, tc.origin); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: tc.method})
			if err != nil {
				t.Fatal("early rejection kept waiting for untrusted body bytes", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("rejection returned %d; want %d", response.StatusCode, tc.status)
			}
		})
	}
}

func TestFullyConsumedJSONKeepsConnectionReusable(t *testing.T) {
	handler := NewServer(Config{MutationsPerMinute: 20}, nil).Handler()
	completed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if r.Method == http.MethodPost {
			// Leave time for net/http's background disconnect reader to observe
			// a deadline before the outer handler finishes the request.
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
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sources", strings.NewReader(`{"url":"not a URL"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid URL returned %d", response.StatusCode)
	}
	if err := <-completed; err != nil {
		t.Fatalf("body cleanup cancelled the reusable connection context: %v", err)
	}
	reused := false
	request, err = http.NewRequest(http.MethodGet, server.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !reused || response.StatusCode != http.StatusOK {
		t.Fatal("body cleanup disabled ordinary keep-alive reuse")
	}
}

func TestRenamedMediaIsNeverServedAsActiveHTML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.media")
	if err := os.WriteFile(path, []byte("<html><script>alert(1)</script></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serveFile(w, httptest.NewRequest("GET", "/media", nil), path, "user-controlled.html", false)
	if content := w.Header().Get("Content-Type"); strings.Contains(content, "html") || strings.Contains(content, "svg") {
		t.Fatalf("active content type %s", content)
	}
}

func TestRateLimitIndependentOfSessionAndTrustedProxy(t *testing.T) {
	s := NewServer(Config{TrustProxy: true}, nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	r.Header.Set("X-Forwarded-For", "attacker, 8.8.8.8")
	if s.clientIP(r) != "8.8.8.8" {
		t.Fatal("did not use final validated proxy address")
	}
	if !s.allow("ip:8.8.8.8", 1) || s.allow("ip:8.8.8.8", 1) {
		t.Fatal("rate limit did not apply")
	}
}
