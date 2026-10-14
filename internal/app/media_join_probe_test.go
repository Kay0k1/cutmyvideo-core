package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Native gate for the accurate-join discovery change. Its oracles are
// known source frame identities/timestamps and decoded audio, not command flags.
// Existing geometry, remote-whitelist, staged-HLS and process-tree cancellation
// gates remain required. Each subtest gets its own deadline on slow native hosts.
func TestJoinedAccurateDiscoveryPreservesReorderedVFRAndSilence(t *testing.T) {
	c, _ := mcConfig(t)
	c.FFmpegThreads = 2
	fixtureRoot := filepath.Join("testdata", "media-contract")
	metadata, err := os.ReadFile(filepath.Join(fixtureRoot, "mono-silence-vfr.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(metadata, &fixture); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(fixtureRoot, fixture.Name)
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if fixture.Name != "mono-silence-vfr.mp4" || hex.EncodeToString(sum[:]) != fixture.SHA256 {
		t.Fatal("pinned mono-silence VFR fixture changed")
	}
	ranges := []Range{{StartMS: 1000, EndMS: 2000, Label: "later"}, {StartMS: 0, EndMS: 1000, Label: "earlier"}}
	// Source PTS 1.04,1.40,1.96 come first, then 0,.04,.20,.32,.60,.80.
	expectedPTS := []float64{.04, .40, .96, 1, 1.04, 1.20, 1.32, 1.60, 1.80}
	expectedColors := [][3]byte{{230, 230, 230}, {70, 70, 70}, {70, 70, 70}, {240, 20, 20}, {20, 240, 20}, {20, 20, 240}, {240, 240, 20}, {20, 240, 240}, {240, 20, 240}}
	for _, silent := range []bool{false, true} {
		name := "selected-audio"
		if silent {
			name = "no-audio"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined", NoAudio: silent}
			out := filepath.Join(c.DataDir, "joined-vfr-"+name+".mp4")
			segments, err := ExportJoinedLocal(ctx, c, input, out, ranges, request)
			if err != nil {
				t.Fatal(err)
			}
			joinedDiscoverySegments(t, segments, ranges)
			joinedDiscoveryPTS(t, mcFramePTS(t, ctx, c, out), expectedPTS)
			mcCheckFrameIDs(t, ctx, c, out, expectedColors)
			mcSquareGeometry(t, ctx, c, out, 32, 24)
			info, _, err := probe(ctx, c, out, false)
			if err != nil {
				t.Fatal(err)
			}
			if (info.selectedAudio() == nil) != silent {
				t.Fatal("joined export ignored NoAudio selection")
			}
			if !silent {
				// Decode all two seconds: the original .600-.700 pulse moves
				// to 1.600-1.700, so a first-second-only check would miss it.
				joinedDiscoveryPulseWindows(t, joinedDiscoveryPCM(t, ctx, c, out, 2000), 2000, [][2]float64{{1.600, 1.700}})
			}
			mcRun(t, ctx, c, "-xerror", "-i", out, "-f", "null", "-")
			joinedDiscoveryWorkspaceRemoved(t, filepath.Dir(out))
		})
	}
}

// Separate progressive streams have independent staging origins. Reversed
// ranges must choose the right video frames and both chirps on their common
// source clock. MP3 exercises the audio-only concat path with the same origins.
func TestJoinedAccurateDiscoveryPreservesDualInputClocks(t *testing.T) {
	c, setupCtx := mcConfig(t)
	c.FFmpegThreads = 2
	video := filepath.Join(c.DataDir, "dual-video.mp4")
	audio := filepath.Join(c.DataDir, "dual-audio.wav")
	mcRun(t, setupCtx, c, "-f", "lavfi", "-i", "testsrc2=duration=4:size=160x96:rate=25", "-an", "-c:v", "libx264", "-threads", "1", "-g", "25", "-bf", "2", "-pix_fmt", "yuv420p", video)
	// A changing-frequency signal avoids periodic-tone correlation ambiguity.
	signal := "aevalsrc=if(between(t\\,1.38\\,1.48)+between(t\\,3.3\\,3.4)\\,0.3*sin(2*PI*(300*t+170*t*t+29*t*t*t))\\,0):s=48000:d=4"
	mcRun(t, setupCtx, c, "-f", "lavfi", "-i", signal, "-c:a", "pcm_s16le", audio)
	// Align PCM starts/ends to 2048-sample packets at 48 kHz. A cut
	// inside a packet retains the established concat rounding behavior.
	inputs := []mediaInput{{Path: video, OffsetMS: 2000}, {Path: audio, OffsetMS: 1000}}
	ranges := []Range{{StartMS: 4200, EndMS: 4712, Label: "later"}, {StartMS: 2280, EndMS: 2792, Label: "earlier"}}
	for _, format := range []string{"mp4", "mp3"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			request := ExportRequest{Format: format, Quality: "best", CutMode: "accurate", OutputMode: "joined"}
			out := filepath.Join(c.DataDir, "joined-dual."+format)
			segments, err := exportJoinedInputsProgress(ctx, c, inputs, ranges, request, out, nil)
			if err != nil {
				t.Fatal(err)
			}
			joinedDiscoverySegments(t, segments, ranges)
			pcm := joinedDiscoveryPCM(t, ctx, c, out, 1024)
			joinedDiscoveryPulseWindows(t, pcm, 1024, [][2]float64{{.100, .200}, {.612, .712}})
			reference := joinedDiscoveryPCM(t, ctx, c, audio, 4000)
			// Compare full chirp windows against independent PCM source data.
			// At 48kHz, 48 samples is exactly the existing 1ms clock gate.
			joinedDiscoveryChirpAlignment(t, reference, pcm, 3250, 50)
			joinedDiscoveryChirpAlignment(t, reference, pcm, 1330, 562)
			if format == "mp4" {
				var expected []float64
				for _, start := range []float64{0, .512} {
					for frame := range 13 {
						expected = append(expected, start+float64(frame)/25)
					}
				}
				joinedDiscoveryPTS(t, mcFramePTS(t, ctx, c, out), expected)
				for i, source := range []int64{2200, 280} {
					want := mcRun(t, ctx, c, "-ss", seconds(source), "-i", video, "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
					got := mcRun(t, ctx, c, "-ss", seconds(int64(i)*512), "-i", out, "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
					if len(got) != len(want) || len(got) == 0 {
						t.Fatal("dual-input boundary frame unavailable")
					}
					var difference int64
					for p, value := range got {
						difference += int64(mcAbs(int(value) - int(want[p])))
					}
					if mean := float64(difference) / float64(len(got)); mean > 5 {
						t.Fatalf("dual-input segment%d starts at wrong video frame: mean %.3f", i, mean)
					}
				}
			}
			mcRun(t, ctx, c, "-xerror", "-i", out, "-f", "null", "-")
			joinedDiscoveryWorkspaceRemoved(t, filepath.Dir(out))
		})
	}
}

func joinedDiscoverySegments(t *testing.T, got []ArtifactSegment, ranges []Range) {
	t.Helper()
	if len(got) != len(ranges) {
		t.Fatal("joined segments lost selected intervals")
	}
	var elapsed int64
	for i, want := range ranges {
		end := elapsed + want.EndMS - want.StartMS
		segment := got[i]
		if segment.Index != i || segment.Label != want.Label || segment.SourceStartMS != want.StartMS || segment.SourceEndMS != want.EndMS || segment.OutputStartMS != elapsed || segment.OutputEndMS != end {
			t.Fatalf("joined segment%d changed clocks/order: %+v", i, segment)
		}
		elapsed = end
	}
}

func joinedDiscoveryPTS(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("joined frame count changed: %v want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > .001 {
			t.Fatalf("joined frame%d timestamp %.6f want %.6f", i, got[i], want[i])
		}
	}
}

func joinedDiscoveryPCM(t *testing.T, ctx context.Context, c Config, path string, durationMS int64) []int16 {
	t.Helper()
	index := mcAudioIndex(t, mcProbe(t, ctx, c, path))
	data := mcRun(t, ctx, c, "-i", path, "-t", seconds(durationMS), "-map", "0:"+strconv.Itoa(index), "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
	if len(data)%2 != 0 || len(data)/2 < int((durationMS-25)*48) {
		t.Fatal("joined decoded audio ended early or is malformed")
	}
	pcm := make([]int16, len(data)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(data[2*i:]))
	}
	return pcm
}

func joinedDiscoveryPulseWindows(t *testing.T, pcm []int16, durationMS int64, windows [][2]float64) {
	t.Helper()
	if len(pcm) < int((durationMS-25)*48) {
		t.Fatal("full joined audio duration unavailable")
	}
	// This is the existing AAC envelope-transient allowance in mcPulseAt,
	// not a relaxed packet/sample clock gate. Frame PTS and independent chirp
	// alignment above retain 1ms checks; primary paired oracles stay unchanged.
	const transient = .012
	seen := make([]bool, len(windows))
	first := make([]int, len(windows))
	last := make([]int, len(windows))
	for sample, value := range pcm {
		if mcAbs(int(value)) <= 500 {
			continue
		}
		at := float64(sample) / 48000
		matched := false
		for i, window := range windows {
			if at >= window[0]-transient && at <= window[1]+transient {
				if !seen[i] {
					first[i] = sample
				}
				seen[i], last[i], matched = true, sample, true
				break
			}
		}
		if !matched {
			t.Fatalf("unexpected joined audio outside requested pulse windows at %.6fs", at)
		}
	}
	for i, window := range windows {
		if !seen[i] || math.Abs(float64(first[i])/48000-window[0]) > transient || math.Abs(float64(last[i])/48000-window[1]) > transient {
			t.Fatalf("joined pulse%d moved/disappeared: samples%d..%d want %.3f..%.3f", i, first[i], last[i], window[0], window[1])
		}
	}
}

func joinedDiscoveryChirpAlignment(t *testing.T, reference, actual []int16, sourceStartMS, outputStartMS int64) {
	t.Helper()
	sourceStart, outputStart := int(sourceStartMS*48), int(outputStartMS*48)
	const count, margin = 14400, 2400 // 300ms, allowing the whole lag search.
	if sourceStart < 0 || outputStart < 0 || sourceStart+count > len(reference) || outputStart+count > len(actual) {
		t.Fatal("independent dual-input chirp window unavailable")
	}
	best, bestLag := -1.0, 0
	for lag := -margin; lag <= margin; lag++ {
		var xy, xx, yy float64
		for i := margin; i < count-margin; i += 8 {
			x, y := float64(reference[sourceStart+i]), float64(actual[outputStart+i+lag])
			xy, xx, yy = xy+x*y, xx+x*x, yy+y*y
		}
		if xx == 0 || yy == 0 {
			continue
		}
		if correlation := xy / math.Sqrt(xx*yy); correlation > best {
			best, bestLag = correlation, lag
		}
	}
	if best < .95 || mcAbs(bestLag) > 48 {
		t.Fatalf("dual-input chirp clock/identity changed: correlation%.6f lag%d/48000s at source%d output%dms", best, bestLag, sourceStartMS, outputStartMS)
	}
}

func joinedDiscoveryWorkspaceRemoved(t *testing.T, parent string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(parent, ".joined-*"))
	if err != nil || len(paths) != 0 {
		t.Fatal("joined intermediate workspace retained", paths, err)
	}
}
