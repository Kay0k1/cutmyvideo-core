package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func TestHealthcheckUsesConfiguredListener(t *testing.T) {
	for _, tc := range []struct{ address, want string }{
		{"", "127.0.0.1:8080"}, {":9000", "127.0.0.1:9000"},
		{"0.0.0.0:9123", "127.0.0.1:9123"}, {"[::]:1234", "[::1]:1234"},
		{"[::1]:9999", "[::1]:9999"}, {"localhost:1234", "localhost:1234"},
	} {
		got, err := healthcheckAddress(tc.address)
		if got != tc.want || err != nil {
			t.Fatalf("address %q: %q %v", tc.address, got, err)
		}
	}
	for _, address := range []string{"8080", ":0", ":65536", ":abc", "::1:1234"} {
		if _, err := healthcheckAddress(address); engine.CodeOf(err) != engine.CodeInvalidArgument {
			t.Fatalf("accepted invalid address %q: %v", address, err)
		}
	}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/readyz" {
				t.Errorf("unexpected probe path: %s", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		err := checkAPIHealth(context.Background(), strings.TrimPrefix(server.URL, "http://"))
		server.Close()
		if (err == nil) != (status == http.StatusOK) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkAPIHealth(ctx, ":8080"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
