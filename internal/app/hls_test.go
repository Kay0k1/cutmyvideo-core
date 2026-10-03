package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type hlsTestTransport func(*http.Request) (*http.Response, error)

func (f hlsTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureGuard(t *testing.T, dir string) (*networkGuard, *[]string) {
	t.Helper()
	g, e := newNetworkGuard(20 << 20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(g.Close)
	requests := &[]string{}
	g.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "media.example" || strings.Contains(r.URL.Path, "..") {
			return nil, errors.New("unexpected fixture address")
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		*requests = append(*requests, name)
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			return nil, e
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b)), Header: http.Header{}}, nil
	})
	return g, requests
}
func TestHLSManifestSecurityAndBoundaries(t *testing.T) {
	base := "https://media.example/recording/index.m3u8?signature=hidden"
	valid := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n0.ts\n#EXTINF:10,\n../1.ts\n#EXTINF:10,\n2.ts\n#EXT-X-ENDLIST\n")
	p, e := parseHLS(base, valid)
	if e != nil {
		t.Fatal(e)
	}
	s, e := selectHLSSegments(p, Range{StartMS: 20500, EndMS: 22000})
	if e != nil || len(s) != 2 || s[0].StartMS != 10000 || s[1].URL != "https://media.example/recording/2.ts" {
		t.Fatalf("wrong middle selection %+v %v", s, e)
	}
	cases := []struct {
		name, manifest string
		err            error
	}{
		{"live", strings.ReplaceAll(string(valid), "#EXT-X-ENDLIST\n", ""), errLiveSource},
		{"file", "#EXTM3U\n#EXTINF:1,\nfile:///etc/passwd\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"http", "#EXTM3U\n#EXTINF:1,\nhttp://127.0.0.1/media\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"credentials", "#EXTM3U\n#EXTINF:1,\nhttps://secret@media.example/a.ts\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"encrypted", "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://127.0.0.1/key\"\n#EXTINF:1,\n0.ts\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"map", "#EXTM3U\n#EXT-X-MAP:URI=\"file:///secret\"\n#EXTINF:1,\n0.m4s\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"nan", "#EXTM3U\n#EXTINF:NaN,\n0.ts\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"negative", "#EXTM3U\n#EXTINF:-1,\n0.ts\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"byterange", "#EXTM3U\n#EXT-X-BYTERANGE:100\n#EXTINF:1,\n0.ts\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"gap", "#EXTM3U\n#EXT-X-GAP\n#EXTINF:1,\n0.ts\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"lowlatency", "#EXTM3U\n#EXT-X-PART:DURATION=0.5,URI=\"0.ts\"\n#EXT-X-ENDLIST", errUnsupportedStream},
		{"oversize", strings.Repeat("x", int(maxManifestBytes+1)), errUnsupportedStream},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, e := parseHLS(base, []byte(tt.manifest)); !errors.Is(e, tt.err) {
				t.Fatalf("got %v want %v", e, tt.err)
			}
		})
	}
	p.Segments[2].Discontinuity = true
	if _, e := selectHLSSegments(p, Range{StartMS: 19000, EndMS: 21000}); e == nil {
		t.Fatal("accepted a discontinuity crossing")
	}
	p.Segments[2].Discontinuity = false
	p.Segments[2].MapURL = "https://media.example/new-init.mp4"
	if _, e := selectHLSSegments(p, Range{StartMS: 19000, EndMS: 21000}); e == nil {
		t.Fatal("accepted an init-map change")
	}
}
func TestHLSFetchBlocksNestedPrivateAddressesAndBudget(t *testing.T) {
	g, e := newNetworkGuard(64)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, raw := range []string{"https://127.0.0.1/segment.ts", "https://169.254.169.254/key", "https://[::1]/init.mp4"} {
		if e := g.fetch(ctx, raw, nil, io.Discard, 100); e == nil {
			t.Fatalf("fetched private HLS address %s", raw)
		}
	}
	g.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: -1, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", 40))), Header: http.Header{}}, nil
	})
	if e := g.fetch(ctx, "https://media.example/first", nil, io.Discard, 100); e != nil {
		t.Fatal(e)
	}
	if e := g.fetch(ctx, "https://media.example/second", nil, io.Discard, 100); e == nil {
		t.Fatal("aggregate HLS transfer budget reset between segments")
	}
}

func TestHLSMiddleExportUsesGlobalTimelineAndRemovesStaging(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, kind := range []string{"mpegts", "fmp4"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(c.DataDir, kind)
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			ext := "ts"
			if kind == "fmp4" {
				ext = "m4s"
			}
			_, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000", "-t", "24", "-c:v", "libx264", "-threads", "1", "-g", "50", "-sc_threshold", "0", "-c:a", "aac", "-f", "hls", "-hls_time", "2", "-hls_playlist_type", "vod", "-hls_segment_type", kind, "-hls_segment_filename", filepath.Join(dir, "segment-%03d."+ext), filepath.Join(dir, "index.m3u8"))
			if e != nil {
				t.Fatal(e)
			}
			g, requests := fixtureGuard(t, dir)
			f := platformFormat{URL: "https://media.example/index.m3u8", Protocol: "m3u8_native", VCodec: "h264", ACodec: "aac", Ext: "mp4"}
			p, e := loadHLS(ctx, g, f, "720p", 0)
			if e != nil {
				t.Fatal(e)
			}
			r := Range{StartMS: 13250, EndMS: 15750}
			stage := t.TempDir()
			path, offset, e := stageHLS(ctx, c, g, f, p, r, stage, 0)
			if e != nil {
				t.Fatal(e)
			}
			if offset < 9800 || offset > 10000 {
				t.Fatalf("wrong original timeline offset %d", offset)
			}
			for _, name := range *requests {
				if strings.HasPrefix(name, "segment-") && name != "segment-005."+ext && name != "segment-006."+ext && name != "segment-007."+ext {
					t.Fatalf("downloaded unselected segment %s", name)
				}
			}
			if _, e := os.Stat(filepath.Join(stage, "stream-0.media")); !os.IsNotExist(e) {
				t.Fatal("raw staging file remained")
			}
			for _, format := range []string{"mp4", "mp3"} {
				out := filepath.Join(stage, "accurate."+format)
				start, end, e := exportInputs(ctx, c, []mediaInput{{Path: path, OffsetMS: offset}}, r, ExportRequest{Format: format, Quality: "720p", CutMode: "accurate"}, out)
				if e != nil || start != 13250 || end < 15700 || end > 15850 {
					t.Fatalf("%s global bounds %d-%d: %v", format, start, end, e)
				}
				if format == "mp4" {
					// Compare the first decoded frame against the original HLS recording.
					// Opening this trusted, generated playlist is confined to the test.
					// Count decoded frames from zero: the HLS demuxer exposes the
					// first AAC packet as format start, which can precede video by
					// codec priming. A second input seek would be a false reference.
					frameNumber := (r.StartMS*25 + 999) / 1000
					original, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", filepath.Join(dir, "index.m3u8"), "-vf", fmt.Sprintf("select=eq(n\\,%d)", frameNumber), "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
					if e != nil {
						t.Fatal(e)
					}
					frame, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
					if e != nil {
						t.Fatal(e)
					}
					if len(original) != len(frame) || len(frame) == 0 {
						t.Fatal("frame comparison unavailable")
					}
					var difference int64
					for i, b := range frame {
						d := int(b) - int(original[i])
						if d < 0 {
							d = -d
						}
						difference += int64(d)
					}
					if mean := float64(difference) / float64(len(frame)); mean > 5 {
						t.Fatalf("first frame is not at the requested original time: mean pixel difference %.3f", mean)
					}
				}
			}
			start, end, e := exportInputs(ctx, c, []mediaInput{{Path: path, OffsetMS: offset}}, r, ExportRequest{Format: "mp4", Quality: "best", CutMode: "copy"}, filepath.Join(stage, "copy.mp4"))
			if e != nil || start < 11900 || start > 12100 || end < 15750 {
				t.Fatalf("copy global keyframe bounds %d-%d: %v", start, end, e)
			}
		})
	}
}
func TestHLSMasterSelectionIsBoundedAndRejectsExternalAudio(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"master.m3u8": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100,RESOLUTION=320x180\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=1920x1080\nhigh.m3u8\n",
		"low.m3u8":    "#EXTM3U\n#EXTINF:2,\nlow.ts\n#EXT-X-ENDLIST\n",
	}
	for name, text := range files {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
	}
	g, requests := fixtureGuard(t, dir)
	p, e := loadHLS(context.Background(), g, platformFormat{URL: "https://media.example/master.m3u8"}, "720p", 0)
	if e != nil || len(p.Segments) != 1 || len(*requests) != 2 || (*requests)[1] != "low.m3u8" {
		t.Fatalf("bad master selection: %+v %v", requests, e)
	}
	if _, e := loadHLS(context.Background(), g, platformFormat{}, "best", 3); !errors.Is(e, errUnsupportedStream) {
		t.Fatal("accepted excessive nesting")
	}
	// External renditions must be supplied as individual extractor formats;
	// selecting only the master video would silently lose its audio.
	data := []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100,AUDIO=\"external\"\nlow.m3u8\n")
	if e := os.WriteFile(filepath.Join(dir, "external.m3u8"), data, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := loadHLS(context.Background(), g, platformFormat{URL: "https://media.example/external.m3u8"}, "best", 0); !errors.Is(e, errUnsupportedStream) {
		t.Fatal("silently omitted external audio")
	}
}

func TestHLSMalformedMediaCleansFiles(t *testing.T) {
	c := mediaConfig(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "bad.ts"), []byte("#EXTM3U\nfile:///etc/passwd"), 0600)
	g, _ := fixtureGuard(t, dir)
	p := hlsPlaylist{DurationMS: 1000, Segments: []hlsSegment{{URL: "https://media.example/bad.ts", StartMS: 0, EndMS: 1000}}}
	stage := t.TempDir()
	if _, _, e := stageHLS(context.Background(), c, g, platformFormat{}, p, Range{EndMS: 1000}, stage, 0); e == nil {
		t.Fatal("accepted untrusted staged playlist")
	}
	entries, e := os.ReadDir(stage)
	if e != nil || len(entries) != 0 {
		t.Fatalf("failed stage left files %+v %v", entries, e)
	}
}

func TestHLSPreservesCombinedIntentionalAudioDelay(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	original := filepath.Join(dir, "original.mp4")
	// A changing chirp makes a time shift measurable; a stationary sine could
	// appear identical after an integer number of cycles. Its intentional160ms
	// offset is part of the original recording, rather than a remux reference.
	_, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-itsoffset", "0.160", "-f", "lavfi", "-i", "aevalsrc=sin(2*PI*(200*t+30*t*t)):s=48000", "-t", "24", "-c:v", "libx264", "-threads", "1", "-g", "50", "-sc_threshold", "0", "-c:a", "aac", "-movflags", "+faststart", original)
	if e != nil {
		t.Fatal(e)
	}
	_, e = runCommand(ctx, c.FFmpeg, "-v", "error", "-i", original, "-c", "copy", "-f", "hls", "-hls_time", "2", "-hls_playlist_type", "vod", "-hls_segment_filename", filepath.Join(dir, "segment-%03d.ts"), filepath.Join(dir, "index.m3u8"))
	if e != nil {
		t.Fatal(e)
	}
	g, _ := fixtureGuard(t, dir)
	format := platformFormat{URL: "https://media.example/index.m3u8", Protocol: "m3u8_native"}
	playlist, e := loadHLS(ctx, g, format, "best", 0)
	if e != nil {
		t.Fatal(e)
	}
	r := Range{StartMS: 13250, EndMS: 15750}
	stage := t.TempDir()
	path, offset, e := stageHLS(ctx, c, g, format, playlist, r, stage, 0)
	if e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(stage, "clip.mp4")
	if _, _, e = exportInputs(ctx, c, []mediaInput{{Path: path, OffsetMS: offset}}, r, ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate"}, out); e != nil {
		t.Fatal(e)
	}
	reference, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-ss", seconds(r.StartMS), "-i", original, "-t", "0.4", "-map", "0:a:0", "-f", "s16le", "-ac", "1", "-ar", "48000", "-")
	if e != nil {
		t.Fatal(e)
	}
	actual, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", out, "-t", "0.4", "-map", "0:a:0", "-f", "s16le", "-ac", "1", "-ar", "48000", "-")
	if e != nil {
		t.Fatal(e)
	}
	// AAC priming and sample rounding may differ by a small number of samples;
	// search only ±10ms. A lost160ms delay cannot satisfy this assertion.
	count := len(reference) / 2
	if len(actual)/2 < count {
		count = len(actual) / 2
	}
	if count < 10000 {
		t.Fatal("audio sample comparison unavailable")
	}
	best := -1.0
	bestLag := 0
	for lag := -480; lag <= 480; lag++ {
		var xy, xx, yy float64
		for i := 480; i < count-480; i++ {
			a := float64(int16(binary.LittleEndian.Uint16(reference[i*2:])))
			b := float64(int16(binary.LittleEndian.Uint16(actual[(i+lag)*2:])))
			xy += a * b
			xx += a * a
			yy += b * b
		}
		if score := xy / math.Sqrt(xx*yy); score > best {
			best = score
			bestLag = lag
		}
	}
	if best < 0.95 {
		t.Fatalf("original audio delay was changed: correlation%.4f lag%d samples", best, bestLag)
	}
}
func TestSeparateHLSRenditionsAreExplicitlyRejected(t *testing.T) {
	info := platformInfo{Formats: []platformFormat{
		{URL: "https://media.example/video.m3u8", Protocol: "m3u8_native", Ext: "mp4", VCodec: "h264", ACodec: "none", Height: 720},
		{URL: "https://media.example/audio.m3u8", Protocol: "m3u8_native", Ext: "m4a", VCodec: "none", ACodec: "aac"},
	}}
	if _, e := pickStreams(info, "720p", "mp4"); !errors.Is(e, errUnsupportedStream) {
		t.Fatal("silently assembled unsynchronised separate renditions")
	}
	if streams, e := pickStreams(info, "720p", "mp3"); e != nil || len(streams) != 1 || streams[0].VCodec != "none" {
		t.Fatal("audio-only HLS must remain available")
	}
}
