package app

import (
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicIPRejectsAdditionalSpecialDestinations(t *testing.T) {
	for _, value := range []string{"192.88.99.1", "::127.0.0.1", "::8.8.8.8", "100::1", "100:0:0:1::1", "2001:2::1", "2001:10::1", "2001:1f:ffff::1", "3fff:fff::1", "5f00::1", "fec0::1"} {
		if publicIP(net.ParseIP(value)) {
			t.Errorf("allowed %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "::ffff:8.8.8.8", "2606:4700:4700::1111", "2001:20::1", "2001:3::1"} {
		if !publicIP(net.ParseIP(value)) {
			t.Errorf("rejected public %s", value)
		}
	}
}

func TestRelaySnapshotsHeadersAndPreservesContentEncoding(t *testing.T) {
	g, err := newNetworkGuard(1024)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	headers := map[string]string{"User-Agent": "original"}
	relay, err := g.RelayWithHeaders("https://media.example/clip", headers)
	if err != nil {
		t.Fatal(err)
	}
	headers["User-Agent"] = "changed"
	g.client.Transport = networkTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("User-Agent") != "original" {
			t.Error("caller mutation changed relay credentials")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": []string{"gzip"}, "Vary": []string{"Accept-Encoding"}}, Body: io.NopCloser(strings.NewReader("compressed"))}, nil
	})
	req := httptest.NewRequest("GET", relay, nil)
	req.URL.Scheme = ""
	req.URL.Host = ""
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Vary") != "Accept-Encoding" || w.Body.String() != "compressed" {
		t.Fatalf("encoded representation changed: %d %v", w.Code, w.Header())
	}
}

func TestGuardRejectsInvalidBudgetBeforeListening(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		if g, err := newNetworkGuard(limit); err == nil {
			g.Close()
			t.Fatal("invalid limit accepted")
		}
	}
}

func TestHLSFetchLargestBoundDoesNotWrapAndNegativeRejectsBeforeNetwork(t *testing.T) {
	var calls int
	g := &networkGuard{limit: 1024, client: &http.Client{Transport: networkTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, ContentLength: 3, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("abc"))}, nil
	})}}
	var dst strings.Builder
	if err := g.fetch(context.Background(), "https://media.example/segment", nil, &dst, math.MaxInt64); err != nil || dst.String() != "abc" {
		t.Fatal("largest limit wrapped", dst.String(), err)
	}
	if err := g.fetch(context.Background(), "https://media.example/segment", nil, io.Discard, -1); err == nil || calls != 1 {
		t.Fatal("invalid budget reached network", calls, err)
	}
}
