package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicIPExcludesInternalAndTransitionNetworks(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.2.3.4", "192.168.1.2", "169.254.169.254", "100.64.2.3", "0.0.0.0", "198.18.0.1", "192.0.2.1", "240.1.2.3", "::1", "::ffff:127.0.0.1", "fd00::1", "fe80::1", "2001:db8::1", "64:ff9b::7f00:1", "2002:7f00:1::"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("allowed %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Errorf("blocked public IP %s", ip)
		}
	}
}

func TestURLPolicyAndRedirectDowngrade(t *testing.T) {
	for _, raw := range []string{"http://example.com/video.mp4", "https://user:pass@example.com/", "https://example.com:8443/x", "file:///etc/passwd", "https://", "https://example.com%2f.internal/"} {
		if _, err := validateURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := validateURL("https://example.com/video.mp4"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	if err := safeClient().CheckRedirect(req, nil); err == nil {
		t.Fatal("allowed HTTPS downgrade")
	}
}

func TestGuardBlocksProxyAndRelayInternalConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := safeDial(ctx, "tcp", "127.0.0.1:443"); err == nil {
		conn.Close()
		t.Fatal("dialed private IP")
	}
	g, err := newNetworkGuard(1024)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	relay, err := g.Relay("https://127.0.0.1/video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(relay)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("relay status %d", resp.StatusCode)
	}
	r := httptest.NewRequest("CONNECT", "http://127.0.0.1:443", nil)
	r.Host = "127.0.0.1:443"
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 407 {
		t.Fatalf("unauthenticated proxy: %d", w.Code)
	}
	r.SetBasicAuth(g.token, "")
	r.Header.Set("Proxy-Authorization", r.Header.Get("Authorization"))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("private CONNECT: %d", w.Code)
	}
}
