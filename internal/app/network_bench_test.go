package app

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
)

var networkBenchmarkAllowed bool

func BenchmarkPublicIP(b *testing.B) {
	addresses := []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("8.8.8.8"), net.ParseIP("2606:4700:4700::1111"), net.ParseIP("2002:7f00:1::"), net.ParseIP("::ffff:192.168.1.2")}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		networkBenchmarkAllowed = publicIP(addresses[i%len(addresses)])
	}
}

type networkBenchmarkWriter struct {
	header http.Header
}

func (w *networkBenchmarkWriter) Header() http.Header { return w.header }
func (w *networkBenchmarkWriter) WriteHeader(int)     {}
func (w *networkBenchmarkWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// A real Range-shaped response, streamed into a non-buffering writer, isolates
// the relay's work from response-recorder allocations and network variability.
func BenchmarkNetworkRelayRange(b *testing.B) {
	payload := bytes.Repeat([]byte("v"), 256<<10)
	u, _ := url.Parse("https://media.example/video.mp4")
	g := &networkGuard{limit: 1<<63 - 1, client: &http.Client{Transport: networkTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{
			"Content-Type": {"video/mp4"}, "Content-Length": {"262144"}, "Content-Range": {"bytes 1048576-1310719/8388608"}, "Accept-Ranges": {"bytes"},
		}, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})}}
	r := &http.Request{Method: http.MethodGet, Header: http.Header{"Range": {"bytes=1048576-1310719"}}, URL: u}
	w := &networkBenchmarkWriter{header: make(http.Header)}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.forward(w, r, u, false, nil)
	}
}
