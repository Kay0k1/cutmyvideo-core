package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerJoinedHLSSegmentsKeepOffsetsOrderAndCleanup(t *testing.T) {
	for _, kind := range []string{"mpegts", "fmp4"} {
		t.Run(kind, func(t *testing.T) {
			c := mediaConfig(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			ext := "ts"
			if kind == "fmp4" {
				ext = "m4s"
			}
			playlistPath := filepath.Join(c.DataDir, "index.m3u8")
			if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000", "-t", "24", "-c:v", "libx264", "-threads", "1", "-g", "50", "-sc_threshold", "0", "-c:a", "aac", "-f", "hls", "-hls_time", "2", "-hls_playlist_type", "vod", "-hls_segment_type", kind, "-hls_segment_filename", filepath.Join(c.DataDir, "segment-%03d."+ext), playlistPath); err != nil {
				t.Fatal(err)
			}
			guard, _ := fixtureGuard(t, c.DataDir)
			stream := platformFormat{URL: "https://media.example/index.m3u8", Protocol: "m3u8_native", VCodec: "h264", ACodec: "aac", Ext: "mp4"}
			playlist, err := loadHLS(ctx, guard, stream, "best", 0)
			if err != nil {
				t.Fatal(err)
			}
			request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined", Ranges: []Range{{StartMS: 13250, EndMS: 14750, Label: "Later"}, {StartMS: 5250, EndMS: 6750, Label: "Earlier"}}}
			dir := filepath.Join(c.DataDir, "staged")
			inputs, err := stageWorkerSegments(ctx, c, request, guard, []platformFormat{stream}, []hlsPlaylist{playlist}, []mediaInput{{}}, request.Ranges, dir, nil)
			if err != nil || len(inputs) != 2 || inputs[0][0].Path == inputs[1][0].Path || inputs[0][0].OffsetMS == inputs[1][0].OffsetMS || inputs[0][0].OffsetMS == 0 || inputs[1][0].OffsetMS == 0 {
				t.Fatal("worker HLS stages lost independent offsets", inputs, err)
			}
			out := filepath.Join(c.DataDir, "joined.mp4")
			segments, err := exportJoinedSegmentInputsProgress(ctx, c, inputs, request.Ranges, request, out, nil)
			if err != nil || len(segments) != 2 || segments[0].SourceStartMS != 13250 || segments[1].SourceStartMS != 5250 || segments[1].OutputStartMS != 1500 {
				t.Fatal("joined HLS export changed actual timeline/order", segments, err)
			}
			if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", out, "-f", "null", "-"); err != nil {
				t.Fatal("joined HLS output does not decode", err)
			}
			for _, segment := range segments {
				frameNumber := (segment.SourceStartMS*25 + 999) / 1000
				original, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", playlistPath, "-vf", fmt.Sprintf("select=eq(n\\,%d)", frameNumber), "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
				if err != nil {
					t.Fatal(err)
				}
				frame, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-ss", seconds(segment.OutputStartMS), "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
				if err != nil || len(frame) == 0 || len(frame) != len(original) {
					t.Fatal("joined HLS frame comparison unavailable", err)
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
					t.Fatalf("HLS segment %d starts at wrong source frame: mean pixel difference %.3f", segment.Index, mean)
				}
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			// Denial in a later selected range removes earlier retained HLS stages.
			failedDir := filepath.Join(c.DataDir, "denied")
			actualTransport := guard.client.Transport
			denied := false
			guard.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "segment-001.") {
					denied = true
					return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("denied")), ContentLength: 6}, nil
				}
				return actualTransport.RoundTrip(r)
			})
			failedRanges := []Range{{StartMS: 13250, EndMS: 14750}, {StartMS: 2500, EndMS: 3500}}
			_, err = stageWorkerSegments(ctx, c, request, guard, []platformFormat{stream}, []hlsPlaylist{playlist}, []mediaInput{{}}, failedRanges, failedDir, nil)
			if err == nil || !denied || !cachedAddressDenied(err) {
				t.Fatal("HLS stage denial lost retry vocabulary", err, denied)
			}
			if _, err := os.Stat(failedDir); !os.IsNotExist(err) {
				t.Fatal("partial joined HLS stages remain after denial", err)
			}
		})
	}
}
