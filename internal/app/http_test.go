package app

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
