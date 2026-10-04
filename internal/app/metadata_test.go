package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These fixtures use the actual extractor's postprocessor and JSON serializer,
// without fetching source pages, captions or media.
func TestPlatformMetadataLargeCaptionResponse(t *testing.T) {
	path := metadataExtractor(t)
	g, err := newNetworkGuard(32 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	fixture := metadataFixture()
	fixture["automatic_captions"] = map[string]any{"en": []any{map[string]any{"url": "https://media.example/captions?" + strings.Repeat("a", 9<<20), "ext": "vtt"}}}
	fixture["subtitles"] = map[string]any{"ru": []any{map[string]any{"url": "https://media.example/subtitles", "ext": "vtt"}}}
	input := writeMetadataFixture(t, fixture)
	args := metadataFixtureArgs(input, g.ProxyURL())

	// The previously used extraction contract hits the real 8 MiB stdout bound.
	var baseline []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--parse-metadata" {
			i++
			continue
		}
		baseline = append(baseline, args[i])
	}
	if _, err := runMetadataFixture(t, path, baseline); err == nil || err.Error() != "process response is too large" {
		t.Fatalf("large caption fixture did not reproduce the bounded response failure: %v", err)
	}
	b, err := runMetadataFixture(t, path, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 64<<10 {
		t.Fatal("unused captions remained in the response")
	}
	var result map[string]any
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"automatic_captions", "subtitles"} {
		if result[name] != "" {
			t.Fatalf("unused %s was not cleared before serialization", name)
		}
	}
	var info platformInfo
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	if err := validatePlatformInfo(info); err != nil {
		t.Fatal(err)
	}
	if info.ID != "fixture" || info.Title != "Recording fixture" || info.Duration != 1200 || info.Extractor != "Youtube" || info.Thumbnail != "https://media.example/thumbnail.jpg" || info.Type != "video" || info.IsLive || info.LiveStatus != "not_live" || info.HasDRM {
		t.Fatal("required recording metadata changed")
	}
	formats := fixture["formats"].([]platformFormat)
	if len(info.Formats) != len(formats) {
		t.Fatal("stream metadata was dropped")
	}
	byID := make(map[string]platformFormat)
	for _, f := range info.Formats {
		byID[f.ID] = f
	}
	for _, want := range formats {
		got, ok := byID[want.ID]
		if !ok || got.URL != want.URL || got.Protocol != want.Protocol || got.VCodec != want.VCodec || got.ACodec != want.ACodec || got.Height != want.Height || got.Width != want.Width || got.ABR != want.ABR || got.TBR != want.TBR || got.Ext != want.Ext || got.HasDRM != want.HasDRM {
			t.Fatal("complete stream metadata did not survive caption removal")
		}
		for key, value := range want.Headers {
			if got.Headers[key] != value {
				t.Fatal("stream request headers changed")
			}
		}
	}
	selected, err := pickStreams(info, "1080p", "mp4")
	if err != nil || len(selected) != 2 || selected[0].ID != "303" || selected[1].ID != "251" {
		t.Fatal("compatible stream selection changed")
	}
	if g.bytes.Load() != 0 {
		t.Fatal("offline metadata fixture made a network request")
	}
}

func TestPlatformMetadataTransformPreservesRejections(t *testing.T) {
	path := metadataExtractor(t)
	g, err := newNetworkGuard(32 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, tt := range []struct {
		name   string
		change func(map[string]any)
		want   error
	}{
		{"playlist", func(info map[string]any) { info["_type"] = "playlist"; info["entries"] = []any{metadataFixture()} }, errCollection},
		{"multi-video", func(info map[string]any) { info["_type"] = "multi_video"; info["entries"] = []any{metadataFixture()} }, errCollection},
		{"nested-collection", func(info map[string]any) {
			info["_type"] = "playlist"
			nested := metadataFixture()
			nested["_type"], nested["entries"] = "playlist", []any{metadataFixture()}
			info["entries"] = []any{nested}
		}, errCollection},
		{"empty-collection", func(info map[string]any) { info["_type"] = "playlist"; info["entries"] = []any{} }, errCollection},
		{"live", func(info map[string]any) { info["is_live"] = true; info["live_status"] = "is_live" }, errLiveSource},
		{"upcoming", func(info map[string]any) { info["live_status"] = "is_upcoming" }, errLiveSource},
		{"drm", func(info map[string]any) { info["has_drm"] = true }, errUnsupportedStream},
		{"missing-duration", func(info map[string]any) { delete(info, "duration") }, errPlatformUnavailable},
		{"duration-limit", func(info map[string]any) { info["duration"] = 31 * 24 * 3600 }, errPlatformUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := metadataFixture()
			tt.change(fixture)
			b, err := runMetadataFixture(t, path, metadataFixtureArgs(writeMetadataFixture(t, fixture), g.ProxyURL()))
			if err != nil {
				t.Fatal(err)
			}
			var info platformInfo
			if err := json.Unmarshal(b, &info); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(validatePlatformInfo(info), tt.want) {
				t.Fatal("metadata transformation bypassed source rejection")
			}
			if tt.want == errCollection && info.Type != fixture["_type"] {
				t.Fatal("collection root envelope was replaced by an entry")
			}
		})
	}
	if g.bytes.Load() != 0 {
		t.Fatal("offline metadata fixtures made a network request")
	}
}

func TestPlatformMetadataRequiredFieldsRemainBounded(t *testing.T) {
	path := metadataExtractor(t)
	g, err := newNetworkGuard(32 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	fixture := metadataFixture()
	formats := fixture["formats"].([]platformFormat)
	formats[0].Headers["X-Fixture"] = strings.Repeat("a", 9<<20)
	if _, err := runMetadataFixture(t, path, metadataFixtureArgs(writeMetadataFixture(t, fixture), g.ProxyURL())); err == nil || err.Error() != "process response is too large" {
		t.Fatalf("caption removal weakened the stdout limit for required metadata: %v", err)
	}
	if g.bytes.Load() != 0 {
		t.Fatal("offline metadata fixture made a network request")
	}
}

func metadataExtractor(t *testing.T) string {
	t.Helper()
	path := os.Getenv("CUTMY_TEST_YTDLP")
	explicit := path != ""
	if !explicit {
		path = "yt-dlp"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		if explicit {
			t.Fatal("configured CUTMY_TEST_YTDLP is not executable")
		}
		t.Skip("yt-dlp is not installed; CI provides the pinned extractor")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := runCommand(ctx, resolved, "--version")
	if err != nil || strings.TrimSpace(string(version)) != "2026.08.19" {
		t.Fatal("metadata regressions require the pinned yt-dlp 2026.08.19")
	}
	return resolved
}

func metadataFixture() map[string]any {
	return map[string]any{
		"id": "fixture", "title": "Recording fixture", "duration": 1200,
		"extractor": "youtube", "extractor_key": "Youtube", "_type": "video",
		"thumbnail": "https://media.example/thumbnail.jpg", "is_live": false,
		"live_status": "not_live", "has_drm": false,
		"formats": []platformFormat{
			{ID: "303", URL: "https://media.example/video.webm", Protocol: "https", VCodec: "vp9", ACodec: "none", Height: 1080, Width: 1920, TBR: 2500, Ext: "webm", Headers: map[string]string{"User-Agent": "fixture-agent", "Referer": "https://media.example/recording"}},
			{ID: "251", URL: "https://media.example/audio.webm", Protocol: "https", VCodec: "none", ACodec: "opus", ABR: 128, TBR: 128, Ext: "webm", Headers: map[string]string{"User-Agent": "fixture-agent", "X-Fixture": "audio"}},
			{ID: "312", URL: "https://media.example/video.m3u8", Protocol: "m3u8_native", VCodec: "avc1.64002a", ACodec: "none", Height: 1080, Width: 1920, TBR: 6000, Ext: "mp4", Headers: map[string]string{"X-Fixture": "video"}},
		},
	}
}

func writeMetadataFixture(t *testing.T, fixture map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.info.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func metadataFixtureArgs(input, proxy string) []string {
	args := platformMetadataArgs("https://www.youtube.com/watch?v=N3REfH4N9Dg", proxy)
	// Loading info JSON otherwise strips entries before the real postprocessors.
	// Production page extraction keeps its normal JSON cleanup behavior.
	return append(args[:len(args)-2], "--no-clean-info-json", "--load-info-json", input)
}

func runMetadataFixture(t *testing.T, path string, args []string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, path, args...)
}
