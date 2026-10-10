package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Assert decoded content, not just container duration: a copy can report the
// right duration while silently dropping the selected B-frame keyframe or
// shifting its audio. Separate progressive inputs exercise both input clocks.
func TestCopyPreservesFirstKeyframeAndAudioTimeline(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base := filepath.Join(c.DataDir, "source.mp4")
	delayed := filepath.Join(c.DataDir, "delayed.mp4")
	for _, fixture := range []struct {
		path  string
		delay bool
	}{{base, false}, {delayed, true}} {
		args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25"}
		if fixture.delay {
			args = append(args, "-itsoffset", "0.160")
		}
		args = append(args, "-f", "lavfi", "-i", "aevalsrc=sin(2*PI*(200*t+30*t*t)):s=48000", "-t", "8", "-c:v", "libx264", "-threads", "1", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-bf", "2", "-c:a", "aac", fixture.path)
		if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
			t.Fatal(err)
		}
	}
	mkv, delayedMKV := filepath.Join(c.DataDir, "source.mkv"), filepath.Join(c.DataDir, "delayed.mkv")
	video, audio := filepath.Join(c.DataDir, "video.mp4"), filepath.Join(c.DataDir, "audio.m4a")
	for _, args := range [][]string{
		{"-v", "error", "-i", base, "-c", "copy", mkv},
		{"-v", "error", "-i", delayed, "-c", "copy", delayedMKV},
		{"-v", "error", "-i", base, "-map", "0:v:0", "-c", "copy", video},
		{"-v", "error", "-i", base, "-map", "0:a:0", "-c", "copy", audio},
	} {
		if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
			t.Fatal(err)
		}
	}

	for _, fixture := range []struct {
		name           string
		inputs         []string
		audioReference string
		missingDTS     bool
	}{
		{"mp4", []string{base}, base, false},
		{"mkv", []string{mkv}, mkv, true},
		{"separate-progressive-audio", []string{video, audio}, base, false},
		{"mp4-intentional-audio-delay", []string{delayed}, delayed, false},
		{"mkv-intentional-audio-delay", []string{delayedMKV}, delayedMKV, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			packet, err := runCommand(ctx, c.FFprobe, "-v", "error", "-select_streams", "v:0", "-read_intervals", "%+#1", "-show_packets", "-show_entries", "packet=pts_time,dts_time", "-of", "json", fixture.inputs[0])
			if err != nil {
				t.Fatal(err)
			}
			var first struct {
				Packets []struct {
					PTS string  `json:"pts_time"`
					DTS *string `json:"dts_time"`
				} `json:"packets"`
			}
			if err := json.Unmarshal(packet, &first); err != nil || len(first.Packets) != 1 {
				t.Fatalf("first packet unavailable: %s, %v", packet, err)
			}
			if (first.Packets[0].DTS == nil) != fixture.missingDTS {
				t.Fatalf("fixture does not exercise expected DTS branch: %s", packet)
			}
			if first.Packets[0].DTS != nil {
				dts, err := strconv.ParseFloat(*first.Packets[0].DTS, 64)
				if err != nil || dts >= 0 {
					t.Fatalf("fixture has no initial negative DTS: %s", packet)
				}
			}
			firstPTS, err := strconv.ParseFloat(first.Packets[0].PTS, 64)
			if err != nil {
				t.Fatal(err)
			}
			for _, requested := range []int64{0, 100, 3500} {
				t.Run(fmt.Sprintf("start-%d", requested), func(t *testing.T) {
					out := filepath.Join(c.DataDir, fmt.Sprintf("%s-%d.mp4", fixture.name, requested))
					r := Range{StartMS: requested, EndMS: 5000}
					start, end, err := exportMedia(ctx, c, fixture.inputs, false, r, ExportRequest{Format: "mp4", Quality: "best", CutMode: "copy"}, out)
					if err != nil {
						t.Fatal(err)
					}
					keyframe := int64(0)
					if requested == 3500 {
						keyframe = 2000
					}
					if start != int64(math.Round(firstPTS*1000))+keyframe || end < 4950 || end > 5300 {
						t.Fatalf("copy bounds %d-%d do not describe selected keyframe", start, end)
					}
					reference, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", base, "-vf", fmt.Sprintf("select=eq(n\\,%d)", keyframe*25/1000), "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
					if err != nil {
						t.Fatal(err)
					}
					actual, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
					if err != nil || len(actual) == 0 || !bytes.Equal(reference, actual) {
						t.Fatalf("first decoded frame does not match selected keyframe: %v", err)
					}
					// Decode from zero rather than seeking the reference: input -ss
					// can itself discard AAC priming or normalize its initial delay.
					// first_pts=0 retains silence for an intentional audio offset.
					readPCM := func(path string, from int64) []byte {
						filter := fmt.Sprintf("aresample=async=1:first_pts=0,atrim=start=%s:end=%s,asetpts=PTS-STARTPTS", seconds(from), seconds(from+400))
						pcm, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", path, "-map", "0:a:0", "-af", filter, "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
						if err != nil {
							t.Fatal(err)
						}
						return pcm
					}
					refPCM, gotPCM := readPCM(fixture.audioReference, start+200), readPCM(out, 200)
					count := min(len(refPCM), len(gotPCM)) / 2
					if count < 10000 {
						t.Fatal("audio reference is too short")
					}
					best, bestLag := -1.0, 0
					for lag := -480; lag <= 480; lag++ {
						var xy, xx, yy float64
						for i := 480; i < count-480; i += 4 {
							x := float64(int16(binary.LittleEndian.Uint16(refPCM[i*2:])))
							y := float64(int16(binary.LittleEndian.Uint16(gotPCM[(i+lag)*2:])))
							xy += x * y
							xx += x * x
							yy += y * y
						}
						if score := xy / math.Sqrt(xx*yy); score > best {
							best, bestLag = score, lag
						}
					}
					if best < 0.99 || math.Abs(float64(bestLag)) > 48 {
						t.Fatalf("copy changed audio alignment: correlation %.5f lag %d samples", best, bestLag)
					}
				})
			}
		})
	}
}
