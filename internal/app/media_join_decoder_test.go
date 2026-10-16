package app

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Observe actual subprocess arguments and decode the real output. The resource
// policy must not reduce encoder/filter budgets or affect audio-only inputs.
// Existing joined discovery tests independently check frame/PCM identities,
// VFR, silence, input offsets, reordered ranges and segment maps.
func TestJoinedAccurateDiscoveryDecoderBudgetPreservesEncoding(t *testing.T) {
	c, setupCtx := mcConfig(t)
	input := batchFixture(t, c, setupCtx)
	audio := filepath.Join(c.DataDir, "decoder-budget-audio.wav")
	mcRun(t, setupCtx, c, "-i", input, "-map", "0:a:0", "-vn", "-c:a", "pcm_s16le", audio)
	observed := c
	commands := countMediaCommands(t, &observed)
	seenCommands := 0
	ranges := []Range{{StartMS: 2000, EndMS: 2500, Label: "later"}, {StartMS: 1000, EndMS: 1500, Label: "earlier"}}
	for _, tc := range []struct {
		name     string
		budget   int
		format   string
		draft    bool
		separate bool
	}{
		{"one", 1, "mp4", false, false},
		{"two-final", 2, "mp4", false, false},
		{"two-draft", 2, "mp4", true, false},
		{"explicit-four", 4, "mp4", false, false},
		{"fallback-default", 0, "mp4", false, false},
		{"fallback-out-of-range", 33, "mp4", false, false},
		{"audio-only", 2, "mp3", false, false},
		{"separate-audio", 2, "mp4", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if tc.draft {
				ctx = CollectionRenderContext(ctx, "draft")
			}
			config := observed
			config.FFmpegThreads = tc.budget
			request := ExportRequest{Format: tc.format, Quality: "best", CutMode: "accurate", OutputMode: "joined"}
			inputs := []mediaInput{{Path: input}}
			if tc.separate {
				inputs = append(inputs, mediaInput{Path: audio})
			}
			out := filepath.Join(c.DataDir, "decoder-budget-"+tc.name+"."+tc.format)
			segments, err := exportJoinedInputsProgress(ctx, config, inputs, ranges, request, out, nil)
			if err != nil {
				t.Fatal(err)
			}
			joinedDiscoverySegments(t, segments, ranges)
			var actual []string
			allCommands := commands()
			recentCommands := allCommands[seenCommands:]
			seenCommands = len(allCommands)
			for _, command := range recentCommands {
				if strings.Contains(command, " -c:v libx264 ") || strings.Contains(command, " -c:a libmp3lame ") {
					if actual != nil {
						t.Fatal("joined edit used more than one encode")
					}
					actual = strings.Fields(command)
				}
			}
			if actual == nil {
				t.Fatal("real subprocess witness did not record the encoder")
			}
			value := func(flag string) string {
				t.Helper()
				for i, arg := range actual {
					if arg == flag && i+1 < len(actual) {
						return actual[i+1]
					}
				}
				t.Fatalf("actual encoder did not receive %s", flag)
				return ""
			}
			lastInput := -1
			for i, arg := range actual {
				if arg == "-i" {
					lastInput = i
				}
			}
			var decoderThreads []string
			outputThreads := ""
			for i, arg := range actual {
				if arg != "-threads" || i+1 >= len(actual) {
					continue
				}
				if i < lastInput {
					decoderThreads = append(decoderThreads, actual[i+1])
				} else {
					if outputThreads != "" {
						t.Fatal("encoder received duplicate output thread budgets")
					}
					outputThreads = actual[i+1]
				}
			}
			budget := tc.budget
			if budget < 1 || budget > 32 {
				budget = 2
			}
			wantDecoder := budget
			if tc.format != "mp3" && budget <= 2 {
				wantDecoder = 1
			}
			want := []string{strconv.Itoa(wantDecoder)}
			if tc.separate {
				want = append(want, strconv.Itoa(budget))
			}
			if strings.Join(decoderThreads, ",") != strings.Join(want, ",") || outputThreads != strconv.Itoa(budget) || value("-filter_threads") != strconv.Itoa(budget) {
				t.Fatalf("actual input/output/filter budgets changed: decoder=%v want=%v encoder=%s filter=%s", decoderThreads, want, outputThreads, value("-filter_threads"))
			}
			if tc.format != "mp3" && (value("-c:a") != "aac" || value("-b:a") != "128k") {
				t.Fatal("video export changed audio encoder")
			}
			if tc.format == "mp3" {
				if value("-c:a") != "libmp3lame" || value("-b:a") != "192k" {
					t.Fatal("audio-only export changed encoding settings")
				}
			} else {
				crf := "18"
				if tc.draft {
					crf = "28"
				}
				if value("-c:v") != "libx264" || value("-preset") != "ultrafast" || value("-crf") != crf || value("-pix_fmt") != "yuv420p" || value("-coder") != "1" {
					t.Fatal("decoder budget changed output encoding policy")
				}
				mcSquareGeometry(t, ctx, c, out, 160, 90)
			}
			mcRun(t, ctx, c, "-xerror", "-i", out, "-f", "null", "-")
		})
	}
}
