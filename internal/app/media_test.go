package app

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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

func TestStreamSelectionRejectsSegmentedAndPrivateProtocols(t *testing.T) {
	info := platformInfo{Formats: []platformFormat{{ID: "hls", URL: "https://example.com/list.m3u8", Protocol: "m3u8_native", VCodec: "h264", ACodec: "aac", Height: 720, Ext: "mp4"}}}
	if _, err := pickStreams(info, "720p", "mp4"); err == nil {
		t.Fatal("selected HLS playlist")
	}
	info.Formats = append(info.Formats, platformFormat{ID: "public", URL: "https://example.com/video.mp4", Protocol: "https", VCodec: "h264", ACodec: "aac", Height: 720, Ext: "mp4"})
	streams, err := pickStreams(info, "720p", "mp4")
	if err != nil || len(streams) != 1 || streams[0].ID != "public" {
		t.Fatalf("unexpected streams %+v %v", streams, err)
	}
}
