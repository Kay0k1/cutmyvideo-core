package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func continuityFixture(t *testing.T, c Config, changed ...bool) string {
	t.Helper()
	root := t.TempDir()
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:2\n")
	for i, group := range []struct{ color, duration, tone string }{{"red", "6", "440"}, {"green", "0.274", "880"}, {"blue", "6", "1320"}} {
		dir := filepath.Join(root, "group-"+strconv.Itoa(i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		height := "90"
		if i == 2 && len(changed) > 0 && changed[0] {
			height = "120"
		}
		_, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color="+group.color+":size=160x"+height+":rate=25", "-f", "lavfi", "-i", "sine=frequency="+group.tone+":sample_rate=48000", "-t", group.duration, "-c:v", "libx264", "-threads", "1", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-bf", "2", "-refs", "1", "-c:a", "aac", "-f", "hls", "-hls_time", "2", "-hls_playlist_type", "vod", "-hls_segment_type", "fmp4", "-hls_segment_filename", filepath.Join(dir, "%02d.m4s"), filepath.Join(dir, "index.m3u8"))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			manifest.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		body, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "#EXT-X-MAP:") {
				manifest.WriteString(strings.Replace(line, "URI=\"", "URI=\"group-"+strconv.Itoa(i)+"/", 1) + "\n")
			} else if strings.HasPrefix(line, "#EXTINF:") {
				if i == 1 {
					line = "#EXTINF:0.274,"
				}
				manifest.WriteString(line + "\n")
			} else if line != "" && !strings.HasPrefix(line, "#") {
				fmt.Fprintf(&manifest, "group-%d/%s\n", i, line)
			}
		}
	}
	manifest.WriteString("#EXT-X-ENDLIST\n")
	if err := os.WriteFile(filepath.Join(root, "index.m3u8"), []byte(manifest.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestHLSContinuityPreservesTinyBridgeVideoAndAudioTimeline(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fixture := continuityFixture(t, c)
	guard, _ := fixtureGuard(t, fixture)
	format := platformFormat{URL: "https://media.example/index.m3u8", Protocol: "m3u8_native", VCodec: "h264", ACodec: "aac", Ext: "mp4"}
	playlist, err := loadHLS(ctx, guard, format, "1080p", 0)
	if err != nil {
		t.Fatal(err)
	}
	selected := Range{StartMS: 4500, EndMS: 9000}
	staging := t.TempDir()
	path, offset, err := stageHLS(ctx, c, guard, format, playlist, selected, staging, 0)
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(staging); len(entries) != 1 || entries[0].Name() != "stream-0.mkv" {
		t.Fatalf("continuity workspace leaked groups or manifest: %+v", entries)
	}
	for _, mode := range []string{"accurate", "copy"} {
		t.Run(mode, func(t *testing.T) {
			output := filepath.Join(c.DataDir, mode+"-continuity.mp4")
			start, end, err := exportInputs(ctx, c, []mediaInput{{Path: path, OffsetMS: offset}}, selected, ExportRequest{Format: "mp4", Quality: "1080p", CutMode: mode}, output)
			if err != nil || math.Abs(float64(end-selected.EndMS)) > 350 {
				t.Fatalf("continuity export %d-%d: %v", start, end, err)
			}
			frames, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", output, "-vf", "scale=1:1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
			if err != nil {
				t.Fatal(err)
			}
			firstGreen, firstBlue, greens := -1, -1, 0
			for i := 0; i+2 < len(frames); i += 3 {
				r, g, b := frames[i], frames[i+1], frames[i+2]
				if g > r && g > b {
					greens++
					if firstGreen < 0 {
						firstGreen = i / 3
					}
				}
				if b > r && b > g && firstBlue < 0 {
					firstBlue = i / 3
				}
			}
			if greens < 6 || greens > 8 || math.Abs(float64(firstGreen)*40-float64(6000-start)) > 45 || math.Abs(float64(firstBlue)*40-float64(6274-start)) > 45 {
				t.Fatalf("bridge frames shifted/lost: start=%d green=%d blue=%d count=%d", start, firstGreen, firstBlue, greens)
			}
			for _, sample := range []struct{ position, tone int64 }{{5500, 440}, {6130, 880}, {6800, 1320}} {
				pcm, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-ss", seconds(sample.position-start), "-i", output, "-t", "0.08", "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "-")
				if err != nil {
					t.Fatal(err)
				}
				bestTone, bestPower := int64(0), float64(0)
				for _, tone := range []int64{440, 880, 1320} {
					var re, im float64
					for n := 0; n+1 < len(pcm); n += 2 {
						value := float64(int16(binary.LittleEndian.Uint16(pcm[n : n+2])))
						angle := 2 * math.Pi * float64(tone) * float64(n/2) / 16000
						re += value * math.Cos(angle)
						im += value * math.Sin(angle)
					}
					if power := re*re + im*im; power > bestPower {
						bestPower, bestTone = power, tone
					}
				}
				if bestTone != sample.tone {
					t.Fatalf("audio drift at%dms: tone%d, want%d", sample.position, bestTone, sample.tone)
				}
			}
		})
	}
}

func TestHLSContinuityGroupsAreBounded(t *testing.T) {
	segments := make([]hlsSegment, 65)
	for i := range segments {
		segments[i].Discontinuity = true
	}
	if _, err := splitHLSContinuity(segments); err == nil {
		t.Fatal("unbounded remux periods accepted")
	}

}

func TestHLSContinuityRejectsChangedCodecAndCleansFailedGroups(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed-codec-%t", changed), func(t *testing.T) {
			c := mediaConfig(t)
			fixture := continuityFixture(t, c, changed)
			guard, _ := fixtureGuard(t, fixture)
			format := platformFormat{URL: "https://media.example/index.m3u8", Protocol: "m3u8_native"}
			playlist, err := loadHLS(context.Background(), guard, format, "1080p", 0)
			if err != nil {
				t.Fatal(err)
			}
			selected := Range{StartMS: 4500, EndMS: 9000}
			if !changed {
				segments, err := selectHLSSegments(playlist, selected)
				if err != nil {
					t.Fatal(err)
				}
				groups, err := splitHLSContinuity(segments)
				if err != nil {
					t.Fatal(err)
				}
				var bytes int64
				for _, group := range groups {
					addresses := []string{group[0].MapURL}
					for _, segment := range group {
						addresses = append(addresses, segment.URL)
					}
					for _, address := range addresses {
						u, _ := url.Parse(address)
						stat, err := os.Stat(filepath.Join(fixture, strings.TrimPrefix(u.Path, "/")))
						if err != nil {
							t.Fatal(err)
						}
						bytes += stat.Size()
					}
				}
				c.MaxFetchBytes = bytes - 1 // Every period alone fits, their aggregate does not.
			}
			staging := t.TempDir()
			if _, _, err := stageHLS(context.Background(), c, guard, format, playlist, selected, staging, 0); err == nil {
				t.Fatal("accepted changed codec or an aggregate staging overflow")
			}
			if entries, _ := os.ReadDir(staging); len(entries) != 0 {
				t.Fatalf("failed periods leaked files: %+v", entries)
			}
		})
	}
}
