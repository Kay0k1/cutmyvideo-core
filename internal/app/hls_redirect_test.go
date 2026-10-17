package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHLSManifestRedirectResolvesFinalRelativeReferences(t *testing.T) {
	type response struct{ location, body string }
	media := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4?map=1\"\n#EXTINF:10,\nsegments/000.m4s?segment=1\n#EXT-X-ENDLIST\n"
	for _, test := range []struct {
		name, entry, segment, initialization string
		responses                            map[string]response
		requests                             []string
	}{
		{
			name: "media playlist", entry: "https://media.example/entry.m3u8",
			segment:        "https://cdn.example/final/segments/000.m4s?segment=1",
			initialization: "https://cdn.example/final/init.mp4?map=1",
			responses: map[string]response{
				"https://media.example/entry.m3u8":                 {location: "https://cdn.example/final/index.m3u8?signature=1"},
				"https://cdn.example/final/index.m3u8?signature=1": {body: media},
			},
			requests: []string{"https://media.example/entry.m3u8", "https://cdn.example/final/index.m3u8?signature=1"},
		},
		{
			name: "master and variant playlists", entry: "https://media.example/entry.m3u8",
			segment:        "https://cdn.example/final/segments/000.m4s?segment=1",
			initialization: "https://cdn.example/final/init.mp4?map=1",
			responses: map[string]response{
				"https://media.example/entry.m3u8":                 {location: "/catalog/master.m3u8"},
				"https://media.example/catalog/master.m3u8":        {body: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1280x720\nvideo/720.m3u8\n"},
				"https://media.example/catalog/video/720.m3u8":     {location: "https://cdn.example/final/index.m3u8?signature=1"},
				"https://cdn.example/final/index.m3u8?signature=1": {body: media},
			},
			requests: []string{"https://media.example/entry.m3u8", "https://media.example/catalog/master.m3u8", "https://media.example/catalog/video/720.m3u8", "https://cdn.example/final/index.m3u8?signature=1"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := safeClient()
			var requests []string
			client.Transport = hlsTestTransport(func(request *http.Request) (*http.Response, error) {
				requests = append(requests, request.URL.String())
				fixture, ok := test.responses[request.URL.String()]
				if !ok {
					t.Fatalf("unexpected manifest request %s", request.URL)
				}
				result := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fixture.body)), ContentLength: int64(len(fixture.body)), Request: request}
				if fixture.location != "" {
					result.StatusCode = http.StatusFound
					result.Header.Set("Location", fixture.location)
				}
				return result, nil
			})
			guard := &networkGuard{client: client, limit: 1 << 20}
			playlist, err := loadHLS(context.Background(), guard, platformFormat{URL: test.entry}, "720p", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(playlist.Segments) != 1 || playlist.Segments[0].URL != test.segment || playlist.Segments[0].MapURL != test.initialization {
				t.Fatalf("relative media references do not use final playlist URL: %+v", playlist)
			}
			if strings.Join(requests, "\n") != strings.Join(test.requests, "\n") {
				t.Fatalf("master/variant requests = %v, want %v", requests, test.requests)
			}
			var expectedBytes int64
			for _, fixture := range test.responses {
				expectedBytes += int64(len(fixture.body))
			}
			if guard.bytes.Load() != expectedBytes {
				t.Fatalf("manifest chain transfer bytes = %d, want %d", guard.bytes.Load(), expectedBytes)
			}
		})
	}
}

func TestHLSManifestRedirectRetainsTargetGuards(t *testing.T) {
	for _, target := range []string{
		"http://media.example/manifest.m3u8",
		"file:///etc/passwd",
		"https://secret@media.example/manifest.m3u8",
		"https://media.example:8443/manifest.m3u8",
		"https://127.0.0.1/manifest.m3u8",
		"https://169.254.169.254/manifest.m3u8",
		"https://[::1]/manifest.m3u8",
	} {
		t.Run(target, func(t *testing.T) {
			client := safeClient()
			transport := client.Transport
			t.Cleanup(client.CloseIdleConnections)
			client.Transport = hlsTestTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() == "https://media.example/entry.m3u8" {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				}
				// The real guarded transport must reject private literal addresses
				// before connecting; invalid URL forms never reach this transport.
				return transport.RoundTrip(request)
			})
			guard := &networkGuard{client: client, limit: 1 << 20}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := loadHLS(ctx, guard, platformFormat{URL: "https://media.example/entry.m3u8"}, "best", 0); err == nil {
				t.Fatal("accepted forbidden redirected manifest target")
			}
		})
	}
}

func TestHLSManifestRedirectRetainsRedirectLimit(t *testing.T) {
	client := safeClient()
	requests := 0
	client.Transport = hlsTestTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"/loop.m3u8"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	guard := &networkGuard{client: client, limit: 1 << 20}
	if _, err := loadHLS(context.Background(), guard, platformFormat{URL: "https://media.example/entry.m3u8"}, "best", 0); err == nil || requests != 5 {
		t.Fatalf("redirect loop was not bounded: requests=%d err=%v", requests, err)
	}
}

func TestHLSManifestRedirectRetainsTransferBudgets(t *testing.T) {
	for _, test := range []struct {
		name                string
		limit, total, prior int64
	}{
		{name: "per response", limit: 3, total: 64},
		{name: "aggregate", limit: 64, total: 6, prior: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := safeClient()
			client.Transport = hlsTestTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/entry.m3u8" {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"/final/index.m3u8"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("four")), ContentLength: -1, Request: request}, nil
			})
			guard := &networkGuard{client: client, limit: test.total}
			guard.bytes.Store(test.prior)
			var output bytes.Buffer
			finalURL, err := guard.fetchWithFinalURL(context.Background(), "https://media.example/entry.m3u8", nil, &output, test.limit)
			if err == nil || finalURL != "" || output.Len() != 0 {
				t.Fatalf("redirect bypassed transfer budget: url=%q output=%q err=%v", finalURL, output.String(), err)
			}
		})
	}
}
