package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyJoinAudioSingleProcessPrototype(t *testing.T) {
	if os.Getenv("CUTMY_COPY_JOIN_PROTOTYPE") != "1" {
		t.Skip("opt-in candidate evaluation, not part of shipped media path")
	}
	testCopyJoinAudioMatrix(t, true)
}

func TestCopyJoinAudioPacketCopyPreservesClocks(t *testing.T) {
	testCopyJoinAudioMatrix(t, false)
}

func testCopyJoinAudioMatrix(t *testing.T, unconditional bool) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	base, delayed := filepath.Join(c.DataDir, "base.mp4"), filepath.Join(c.DataDir, "delayed.mp4")
	for _, fixture := range []struct {
		path  string
		delay bool
	}{{base, false}, {delayed, true}} {
		args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25"}
		if fixture.delay {
			args = append(args, "-itsoffset", "0.160")
		}
		args = append(args, "-f", "lavfi", "-i", "aevalsrc=sin(2*PI*(200*t+30*t*t)):s=48000", "-t", "6", "-c:v", "libx264", "-threads", "1", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-bf", "2", "-c:a", "aac", fixture.path)
		if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
			t.Fatal(err)
		}
	}
	mkv, shifted, delayMKV := filepath.Join(c.DataDir, "base.mkv"), filepath.Join(c.DataDir, "shifted.mkv"), filepath.Join(c.DataDir, "delayed.mkv")
	for _, args := range [][]string{{"-v", "error", "-i", base, "-c", "copy", mkv}, {"-v", "error", "-i", base, "-c", "copy", "-output_ts_offset", "0.040", shifted}, {"-v", "error", "-i", delayed, "-c", "copy", delayMKV}} {
		if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
			t.Fatal(err)
		}
	}
	type audioFixture struct {
		name         string
		inputs       []mediaInput
		reference    string
		refIndex     int
		unknownClock bool
	}
	fixtures := []audioFixture{}
	for i, input := range []string{base, delayed, mkv, shifted, delayMKV} {
		fixtures = append(fixtures, audioFixture{name: fmt.Sprintf("source%d", i), inputs: []mediaInput{{Path: input}}, reference: input, refIndex: 1})
	}
	multi := batchFixture(t, c, ctx)
	fixtures = append(fixtures, audioFixture{name: "selected-non-default-audio", inputs: []mediaInput{{Path: multi}}, reference: multi, refIndex: 2})
	for _, codec := range []struct{ name, encoder string }{{"mp3", "libmp3lame"}, {"opus", "libopus"}, {"flac", "flac"}} {
		path := filepath.Join(c.DataDir, codec.name+".mkv")
		if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", base, "-map", "0:v:0", "-map", "0:a:0", "-c:v", "copy", "-c:a", codec.encoder, "-threads", "1", path); err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, audioFixture{name: codec.name, inputs: []mediaInput{{Path: path}}, reference: path, refIndex: 1})
	}
	videoSource, audioSource := filepath.Join(c.DataDir, "video-source.mp4"), filepath.Join(c.DataDir, "audio-source.m4a")
	for _, args := range [][]string{{"-v", "error", "-i", base, "-map", "0:v:0", "-c", "copy", videoSource}, {"-v", "error", "-i", base, "-map", "0:a:0", "-c", "copy", audioSource}} {
		if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
			t.Fatal(err)
		}
	}
	fixtures = append(fixtures,
		audioFixture{name: "dual-progressive", inputs: []mediaInput{{Path: videoSource}, {Path: audioSource}}, reference: base, refIndex: 1},
		audioFixture{name: "staged-offset", inputs: []mediaInput{{Path: mkv, OffsetMS: 7200000}}, reference: mkv, refIndex: 1},
		audioFixture{name: "unknown-metadata-clock", inputs: []mediaInput{{Path: mkv}}, reference: mkv, refIndex: 1, unknownClock: true})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := base
		if r.URL.Path == "/video" {
			path = videoSource
		} else if r.URL.Path == "/audio" {
			path = audioSource
		}
		http.ServeFile(w, r, path)
	}))
	defer relay.Close()
	fixtures = append(fixtures,
		audioFixture{name: "combined-relay-clock", inputs: []mediaInput{{Path: relay.URL + "/combined", Remote: true}}, reference: base, refIndex: 1},
		audioFixture{name: "dual-relay-clock", inputs: []mediaInput{{Path: relay.URL + "/video", Remote: true}, {Path: relay.URL + "/audio", Remote: true}}, reference: base, refIndex: 1})
	fallbackConfig := c
	fallbackCommands := countMediaCommands(t, &fallbackConfig)
	fallbackCommandCount := 0
	for inputIndex, fixture := range fixtures {
		for _, format := range []string{"mp4", "mkv"} {
			for _, requested := range []int64{0, 100, 3500} {
				t.Run(fmt.Sprintf("%s-%s-start%d", fixture.name, format, requested), func(t *testing.T) {
					inputs := fixture.inputs
					request := ExportRequest{Format: format, Quality: "best", CutMode: "copy"}
					if fixture.refIndex == 2 {
						track := 2
						request.AudioTrack = &track
					}
					video := filepath.Join(c.DataDir, fmt.Sprintf("video-%d-%s-%d.%s", inputIndex, format, requested, format))
					start, end, err := exportInputsCoreProgressMode(ctx, c, inputs, Range{StartMS: requested + inputs[0].OffsetMS, EndMS: 5000 + inputs[0].OffsetMS}, ExportRequest{Format: format, Quality: "best", CutMode: "copy", NoAudio: true}, video, nil, false)
					if err != nil {
						t.Fatal(err)
					}
					selection, err := probeInputSelectionOptions(ctx, c, inputs, request)
					if err != nil {
						t.Fatal(err)
					}
					if fixture.unknownClock {
						selection.Audio.StartTime = "N/A"
					}
					out := filepath.Join(c.DataDir, fmt.Sprintf("prototype-%d-%s-%d.%s", inputIndex, format, requested, format))
					var prototypeErr error
					legacyOpus := !unconditional && selection.Audio.CodecName == "opus" && format == "mkv"
					if unconditional {
						prototypeErr = joinCopyAudioDirect(ctx, c, inputs, selection, start, end, video, out, request, c.MaxOutputBytes)
					} else if legacyOpus {
						prototypeErr = joinCopyAudio(ctx, fallbackConfig, inputs, selection, start, end, video, out, request, c.MaxOutputBytes)
						var stages int
						commands := fallbackCommands()
						for _, line := range commands[fallbackCommandCount:] {
							if strings.Contains(line, "-bsf:a noise=") {
								t.Fatal("Opus/Matroska compatibility path used the unproven direct mux")
							}
							if strings.Contains(line, "audio.mka") {
								stages++
							}
						}
						fallbackCommandCount = len(commands)
						if stages != 2 {
							t.Fatalf("Opus/Matroska compatibility did not use both original stages: %d", stages)
						}
					} else {
						prototypeErr = joinCopyAudio(ctx, c, inputs, selection, start, end, video, out, request, c.MaxOutputBytes)
					}
					baseline := filepath.Join(c.DataDir, fmt.Sprintf("baseline-%d-%s-%d.%s", inputIndex, format, requested, format))
					baselineErr := joinCopyAudioStaged(ctx, c, inputs, selection, start, end, video, baseline, request, c.MaxOutputBytes)
					if baselineErr != nil {
						if prototypeErr == nil || LocalErrorCode(copyExportDiagnostic(prototypeErr, format)) != LocalErrorCode(baselineErr) {
							t.Fatalf("unsupported-container diagnostic changed: baseline=%v prototype=%v", baselineErr, prototypeErr)
						}
						t.Log("Both paths preserve the configured tool's unsupported-container diagnostic")
						return
					}
					if prototypeErr != nil {
						t.Fatal(prototypeErr)
					}
					readPackets := func(path string) []struct {
						Stream int    `json:"stream_index"`
						Hash   string `json:"data_hash"`
					} {
						data, err := runCommand(ctx, c.FFprobe, "-v", "error", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=stream_index,data_hash", "-of", "json", path)
						if err != nil {
							t.Fatal(err)
						}
						var result struct {
							Packets []struct {
								Stream int    `json:"stream_index"`
								Hash   string `json:"data_hash"`
							} `json:"packets"`
						}
						if err := json.Unmarshal(data, &result); err != nil {
							t.Fatal(err)
						}
						return result.Packets
					}
					before, after := readPackets(baseline), readPackets(out)
					for stream := 0; stream < 2; stream++ {
						var expected, actual []string
						for _, packet := range before {
							if packet.Stream == stream {
								expected = append(expected, packet.Hash)
							}
						}
						for _, packet := range after {
							if packet.Stream == stream {
								actual = append(actual, packet.Hash)
							}
						}
						if len(expected) != len(actual) {
							t.Fatalf("stream%d packet count changed: %d -> %d", stream, len(expected), len(actual))
						}
						for i := range expected {
							if expected[i] != actual[i] {
								t.Fatalf("stream%d packet%d payload changed", stream, i)
							}
						}
					}
					if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-xerror", "-i", out, "-f", "null", "-"); err != nil {
						t.Fatal(err)
					}
					if legacyOpus {
						// FFmpeg 5.1's established Opus/Matroska staging already has a
						// source-relative decoder-delay error. This acceleration retains
						// its clock exactly; optimized paths keep the absolute 1 ms gate.
						for _, witness := range []struct{ label, path string }{{"baseline", baseline}, {"candidate", out}} {
							correlation, lag := audioTimelineAlignment(t, ctx, c, fixture.reference, witness.path, fixture.refIndex, start-inputs[selection.AudioInput].OffsetMS+300, 300)
							if correlation < .99 {
								t.Fatalf("legacy Opus/Matroska %s changed source audio: %.6f", witness.label, correlation)
							}
							t.Logf("legacy Opus/Matroska %s source-clock witness: correlation=%.6f lag=%d samples", witness.label, correlation, lag)
						}
						compareAudioTimeline(t, ctx, c, baseline, out, 1, 300, 300, true)
					} else {
						compareAudioTimeline(t, ctx, c, fixture.reference, out, fixture.refIndex, start-inputs[selection.AudioInput].OffsetMS+300, 300, true)
					}
					if _, err := verifyJoined(ctx, c, out, request, end-start, c.MaxOutputBytes); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestCopyPacketDropPreservesUnknownPresentationTimestamps(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := filepath.Join(c.DataDir, "audio.mka")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-t", "0.2", "-c:a", "aac", source); err != nil {
		t.Fatal(err)
	}
	read := func(filter string) []byte {
		args := []string{"-v", "error", "-i", source, "-map", "0:a:0", "-c:a", "copy", "-bsf:a", filter, "-f", "data", "-"}
		data, err := runCommand(ctx, c.FFmpeg, args...)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	unknown := "setts=pts=-9223372036854775808"
	baseline := read(unknown)
	guarded := read(unknown + ",noise=amount=0:drop=lt(pts\\,0)*not(eq(pts\\,nopts))")
	if len(baseline) == 0 || !bytes.Equal(baseline, guarded) {
		t.Fatal("unknown-PTS packets were dropped or modified")
	}
	unguarded := read(unknown + ",noise=amount=0:drop=lt(pts\\,0)")
	if len(unguarded) != 0 {
		t.Fatal("negative control did not distinguish unknown PTS from a negative timestamp")
	}
}
