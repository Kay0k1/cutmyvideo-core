package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMediaFailureCategoriesAreDiagnosticOnly(t *testing.T) {
	for _, v := range []struct {
		stderr, category string
	}{
		{"[tcp] Connection timed out", "network_timeout"},
		{"[tcp] Connection reset by peer", "network_reset"},
		{"[http] HTTP error 503 Service Unavailable", "upstream_unavailable"},
		{"[http] Server returned 429 Too Many Requests", "upstream_unavailable"},
		{"[http] HTTP error 403 Forbidden", "upstream_denied"},
		{"[http] Server returned 404 Not Found", "upstream_denied"},
		{"Stream map '0:a:0' matches no streams.", "missing_audio"},
		{"/data/out.mp4: No space left on device", "storage_full"},
		{"Could not find tag for codec pcm_s16le, codec not currently supported in container", "copy_incompatible"},
		{"Invalid data found when processing input", "unsupported_media"},
		{"Protocol 'file' not on whitelist 'http,tcp'", "unsupported_media"},
		{"Error initializing output stream 0:0", "unknown"},
		{"", "unknown"},
	} {
		t.Run(v.category+v.stderr, func(t *testing.T) {
			if got := mediaFailureCategory(v.stderr); got != v.category {
				t.Fatalf("got %q; want %q", got, v.category)
			}
		})
	}
}

func TestStreamSelectionRanksSupportedPairsBeforeQuality(t *testing.T) {
	// The incident recording offered these families: 1080p HLS video-only 312
	// had a greater bitrate than progressive 299, but only 299 can be paired
	// with separately exposed progressive audio under the current HLS rules.
	video := platformFormat{ID: "299", URL: "https://media.example/video.mp4", Protocol: "https", Ext: "mp4", VCodec: "avc1.64002a", ACodec: "none", Height: 1080, TBR: 3704.8}
	audio := platformFormat{ID: "251", URL: "https://media.example/audio.webm", Protocol: "https", Ext: "webm", VCodec: "none", ACodec: "opus", ABR: 138.5}
	hls := platformFormat{ID: "312", URL: "https://media.example/video.m3u8", Protocol: "m3u8_native", Ext: "mp4", VCodec: "avc1.64002a", ACodec: "none", Height: 1080, TBR: 6253.4}
	combined := platformFormat{ID: "combined", URL: "https://media.example/combined.m3u8", Protocol: "m3u8_native", Ext: "mp4", VCodec: "h264", ACodec: "aac", Height: 720, TBR: 2000}
	hlsAudio := platformFormat{ID: "hls-audio", URL: "https://media.example/audio.m3u8", Protocol: "m3u8_native", Ext: "mp4", VCodec: "none", ACodec: "aac", ABR: 200}
	for _, tt := range []struct {
		name    string
		formats []platformFormat
		quality string
		want    []string
	}{
		{"incident", []platformFormat{audio, hls, video}, "1080p", []string{"299", "251"}},
		{"lower-combined", []platformFormat{hls, hlsAudio, combined}, "1080p", []string{"combined"}},
		{"pair-compatible-audio", []platformFormat{hlsAudio, video, audio}, "1080p", []string{"299", "251"}},
		{"cap-prefers-combined", []platformFormat{hls, video, audio, combined}, "720p", []string{"combined"}},
		{"above-cap-fallback", []platformFormat{hls, video, audio}, "720p", []string{"299", "251"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickStreams(platformInfo{Formats: tt.formats}, tt.quality, "mp4")
			if err != nil || len(got) != len(tt.want) {
				t.Fatalf("selected %+v: %v", got, err)
			}
			for i, f := range got {
				if f.ID != tt.want[i] {
					t.Fatalf("selected %s, want %s", f.ID, tt.want[i])
				}
			}
		})
	}
	if _, err := pickStreams(platformInfo{Formats: []platformFormat{hls, audio}}, "1080p", "mp4"); !errors.Is(err, errUnsupportedStream) {
		t.Fatal("combined unsupported HLS/progressive clocks")
	}
	if _, err := pickStreams(platformInfo{Formats: []platformFormat{video}}, "1080p", "mp3"); !errors.Is(err, errNoAudio) {
		t.Fatal("video-only source did not explain missing audio")
	}
}

func TestMediaCopyQualityAudioMissingAndOutputLimits(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sample := filepath.Join(c.DataDir, "video-only.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x722:rate=25", "-t", "3", "-c:v", "libx264", "-threads", "1", "-g", "25", "-an", "-movflags", "+faststart", sample); err != nil {
		t.Fatal(err)
	}
	r := Range{StartMS: 0, EndMS: 2000}
	request := ExportRequest{Format: "mp4", Quality: "720p", CutMode: "copy"}
	if _, _, err := exportMedia(ctx, c, []string{sample}, false, r, request, filepath.Join(c.DataDir, "copy-capped.mp4")); exportProblem(err).code != "copy_quality_unsupported" {
		t.Fatalf("copy silently exceeded cap: %v", err)
	}
	request.Quality = "best"
	copyPath := filepath.Join(c.DataDir, "copy-original.mp4")
	if _, _, err := exportMedia(ctx, c, []string{sample}, false, r, request, copyPath); err != nil {
		t.Fatal(err)
	}
	p, _, err := probe(ctx, c, copyPath, false)
	if err != nil || len(p.Streams) != 1 || p.Streams[0].Height != 722 {
		t.Fatalf("copy did not preserve resolution: %+v %v", p, err)
	}
	request.Quality, request.CutMode = "720p", "accurate"
	accuratePath := filepath.Join(c.DataDir, "accurate-capped.mp4")
	if _, _, err := exportMedia(ctx, c, []string{sample}, false, r, request, accuratePath); err != nil {
		t.Fatal(err)
	}
	p, _, err = probe(ctx, c, accuratePath, false)
	if err != nil || p.Streams[0].Height != 720 {
		t.Fatalf("accurate did not honor cap: %+v %v", p, err)
	}
	request.Format = "mp3"
	if _, _, err := exportMedia(ctx, c, []string{sample}, false, r, request, filepath.Join(c.DataDir, "no-audio.mp3")); exportProblem(err).code != "audio_missing" {
		t.Fatalf("missing audio lost its diagnostic: %v", err)
	}
	request.Format = "mp4"
	c.MaxOutputBytes = 16 << 10
	if _, _, err := exportMedia(ctx, c, []string{sample}, false, r, request, filepath.Join(c.DataDir, "size-limit.mp4")); exportProblem(err).code != "output_limit" {
		t.Fatalf("output truncation lost its diagnostic: %v", err)
	}
}

func TestMediaSubprocessDiagnosticsAreRedacted(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = runCommand(context.Background(), executable, "-test.run=^TestMediaSubprocessDiagnosticFixture$", "--", "cutmy-private-stderr-fixture")
	var failure *mediaProcessFailure
	if !errors.As(err, &failure) || failure.category != "network_timeout" {
		t.Fatalf("unexpected process failure: %v", err)
	}
	for _, secret := range []string{"signed-secret", "proxy-secret", "video.example", "127.0.0.1"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("private subprocess diagnostic escaped: %v", err)
		}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatal("process exit cause was lost")
	}
}

func TestMediaSubprocessDiagnosticFixture(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "cutmy-private-stderr-fixture" {
			fmt.Fprintln(os.Stderr, "https://video.example/clip?token=signed-secret via http://proxy-secret:proxy-secret@127.0.0.1/ Connection timed out")
			os.Exit(1)
		}
	}
}

func mediaConfig(t *testing.T) Config {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	return Config{FFmpeg: "ffmpeg", FFprobe: "ffprobe", MaxOutputBytes: 10 << 20, MaxSourceBytes: 10 << 20, MaxRanges: 12, MaxRangeMS: 600000, MaxJobMS: 3600000, DataDir: t.TempDir()}
}

func TestMediaExportAccurateMP4MP3AndKeyframeCopy(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sample := filepath.Join(c.DataDir, "source.mp4")
	_, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000", "-t", "8", "-c:v", "libx264", "-threads", "1", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-c:a", "aac", "-movflags", "+faststart", sample)
	if err != nil {
		t.Fatal(err)
	}
	r := Range{StartMS: 3500, EndMS: 6500}
	for _, format := range []string{"mp4", "mp3"} {
		out := filepath.Join(c.DataDir, "accurate."+format)
		start, end, err := exportMedia(ctx, c, []string{sample}, false, r, ExportRequest{Format: format, Quality: "720p", CutMode: "accurate"}, out)
		if err != nil {
			t.Fatal(err)
		}
		if start != 3500 || math.Abs(float64(end-start-3000)) > 100 {
			t.Fatalf("unexpected %s bounds %d-%d", format, start, end)
		}
		p, _, err := probe(ctx, c, out, false)
		if err != nil {
			t.Fatal(err)
		}
		hasAudio := false
		for _, s := range p.Streams {
			if s.CodecType == "audio" {
				hasAudio = true
			}
		}
		if !hasAudio {
			t.Errorf("%s has no audio", format)
		}
	}
	start, end, err := exportMedia(ctx, c, []string{sample}, false, r, ExportRequest{Format: "mp4", Quality: "best", CutMode: "copy"}, filepath.Join(c.DataDir, "copy.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if start != 2000 || end < 6500 || end > 6800 {
		t.Fatalf("copy did not expose keyframe shift: %d-%d", start, end)
	}
}

func TestMediaRejectsPlaylistsBeforeNetwork(t *testing.T) {
	c := mediaConfig(t)
	path := filepath.Join(c.DataDir, "playlist.media")
	if err := os.WriteFile(path, []byte("#EXTM3U\n#EXT-X-TARGETDURATION:5\n#EXTINF:5,\nhttp://127.0.0.1/internal\n#EXT-X-ENDLIST\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := probe(ctx, c, path, false); err == nil {
		t.Fatal("accepted an untrusted network playlist")
	}
}

func TestRangeValidation(t *testing.T) {
	c := Config{MaxRanges: 2, MaxRangeMS: 10000, MaxJobMS: 15000}
	s := Source{DurationMS: 20000}
	valid := ExportRequest{SourceID: "src", Ranges: []Range{{StartMS: 1000, EndMS: 4000}}, Format: "mp4", Quality: "1080p", CutMode: "accurate"}
	if err := valid.Validate(c, s); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Range{{StartMS: -1, EndMS: 1000}, {StartMS: 1000, EndMS: 1000}, {StartMS: 19000, EndMS: 21000}, {StartMS: 0, EndMS: 11000}} {
		req := valid
		req.Ranges = []Range{r}
		if err := req.Validate(c, s); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	valid.Format = "mp3"
	valid.CutMode = "copy"
	if err := valid.Validate(c, s); err == nil {
		t.Fatal("accepted lossy MP3 as stream copy")
	}
}

func TestStreamSelectionAllowsBoundedHLSAndRejectsUnsupportedProtocols(t *testing.T) {
	info := platformInfo{Formats: []platformFormat{{ID: "hls", URL: "https://example.com/list.m3u8", Protocol: "m3u8_native", VCodec: "h264", ACodec: "aac", Height: 720, Ext: "mp4"}}}
	if streams, err := pickStreams(info, "720p", "mp4"); err != nil || len(streams) != 1 || !isHLS(streams[0]) {
		t.Fatalf("finite HLS candidate unavailable: %v", err)
	}
	for _, protocol := range []string{"http", "ftp", "file", "http_dash_segments", "mhtml"} {
		bad := info
		bad.Formats = []platformFormat{{URL: "https://example.com/video", Protocol: protocol, Ext: "mp4", VCodec: "h264", ACodec: "aac"}}
		if _, err := pickStreams(bad, "best", "mp4"); err == nil {
			t.Fatalf("accepted protocol %s", protocol)
		}
	}
	info.Formats = append(info.Formats, platformFormat{ID: "public", URL: "https://example.com/video.mp4", Protocol: "https", VCodec: "h264", ACodec: "aac", Height: 720, Ext: "mp4"})
	streams, err := pickStreams(info, "720p", "mp4")
	if err != nil || len(streams) != 1 || streams[0].ID != "hls" {
		t.Fatalf("unexpected streams %+v %v", streams, err)
	}
}

func TestProgressiveClipsWithUnknownCodecsRemainImportable(t *testing.T) {
	info := platformInfo{Formats: []platformFormat{{ID: "1080", URL: "https://media.example/clip.mp4", Protocol: "https", Ext: "mp4", Height: 1080}}}
	for _, format := range []string{"mp4", "mp3"} {
		streams, e := pickStreams(info, "720p", format)
		if e != nil || len(streams) != 1 || streams[0].ID != "1080" {
			t.Fatalf("unknown codecs mistaken for absent streams: %v", e)
		}
	}
	info.Formats[0].HasDRM = true
	if _, e := pickStreams(info, "best", "mp4"); !errors.Is(e, errUnsupportedStream) {
		t.Fatal("selected DRM clip")
	}
}
