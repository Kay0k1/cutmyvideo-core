package engine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func renderTool(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("independent media tool: %v %s", err, data)
	}
	return data
}

func renderFixture(t *testing.T) (string, string) {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
	dir := t.TempDir()
	video, audio := filepath.Join(dir, "picture.mp4"), filepath.Join(dir, "replacement.wav")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "color=red:size=160x90:rate=25:duration=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", video)
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=1", "-c:a", "pcm_s16le", audio)
	return video, audio
}

func pcmRMS(data []byte, start, end float64) float64 {
	a, b := int(start*48000), int(end*48000)
	if b*2 > len(data) {
		return -1
	}
	var sum float64
	for i := a; i < b; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(data[2*i:])))
		sum += v * v
	}
	return math.Sqrt(sum / float64(b-a))
}

func TestRenderCanvasAndExternalAudioTimelineActualMedia(t *testing.T) {
	video, audio := renderFixture(t)
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	ctx := context.Background()
	plan := Plan{Version: 1, Input: video, OutputMode: "joined", Ranges: []NamedRange{{StartMS: 0, EndMS: 2000}, {StartMS: 3000, EndMS: 4000}, {StartMS: 1000, EndMS: 2000}}, Options: AdvancedOptions{Options: Options{Format: "mp4"}, Canvas: &Canvas{Width: 180, Height: 320, Fit: "contain", Background: "#0000FF"}, AudioReplace: &AudioReplace{Input: audio, OffsetMS: 1000}}}
	output := filepath.Join(t.TempDir(), "joined.mp4")
	preflight, err := e.PreflightPlan(ctx, plan, output)
	if err != nil || preflight.Video == nil || preflight.Video.Width != 180 || preflight.Video.Height != 320 {
		t.Fatalf("preflight: %+v %v", preflight, err)
	}
	result, err := e.ExecutePlan(ctx, plan, output)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || len(result.Outputs[0].Segments) != 3 || result.Outputs[0].ActualEndMS != 4000 {
		t.Fatalf("timeline: %+v", result)
	}
	for _, track := range result.Outputs[0].TrackManifest {
		if track.Type == "audio" && (track.Included || track.Reason != "audio_replaced") {
			t.Fatalf("original audio incorrectly included: %+v", track)
		}
	}
	rgb := renderTool(t, "-v", "error", "-i", output, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	if len(rgb) != 180*320*3 {
		t.Fatalf("canvas frame size: %d", len(rgb))
	}
	corner, center := rgb[:3], rgb[(160*180+90)*3:(160*180+90)*3+3]
	if corner[2] < 220 || corner[0] > 30 || center[0] < 220 || center[2] > 30 {
		t.Fatalf("canvas padding/picture colors: %v %v", corner, center)
	}
	pcm := renderTool(t, "-v", "error", "-i", output, "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
	for _, window := range []struct {
		a, b float64
		tone bool
	}{{.15, .85, false}, {1.15, 1.85, true}, {2.15, 2.85, false}, {3.15, 3.85, true}} {
		rms := pcmRMS(pcm, window.a, window.b)
		if rms < 0 || window.tone && rms < 1500 || !window.tone && rms > 10 {
			t.Fatalf("replacement window %+v RMS=%v", window, rms)
		}
	}
	var fundamental, original float64
	for i := int(1.2 * 48000); i < int(1.7*48000); i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		fundamental += v * math.Sin(2*math.Pi*880*float64(i)/48000)
		original += v * math.Sin(2*math.Pi*440*float64(i)/48000)
	}
	if math.Abs(fundamental) < 100*math.Abs(original) {
		t.Fatal("original audio leaked into external replacement")
	}
	// Negative offsets trim the beginning of the external recording. A wholly
	// missing selection remains a complete silent output rather than shortening.
	for _, offset := range []int64{-500, 8000} {
		out := filepath.Join(t.TempDir(), "offset.mp4")
		_, err = e.ExportWithOptions(ctx, video, out, Range{EndMS: 2000}, AdvancedOptions{Options: Options{Format: "mp4"}, AudioReplace: &AudioReplace{Input: audio, OffsetMS: offset}})
		if err != nil {
			t.Fatal(err)
		}
		pcm = renderTool(t, "-v", "error", "-i", out, "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
		if offset < 0 && pcmRMS(pcm, .1, .4) < 1500 || pcmRMS(pcm, 1.1, 1.8) > 10 || offset > 0 && pcmRMS(pcm, .1, .4) > 10 {
			t.Fatalf("offset %d produced wrong silence/tone windows", offset)
		}
	}
	i, tp, lra := -16.0, -1.0, 11.0
	combined := plan
	combined.Ranges = []NamedRange{{EndMS: 4000}}
	combined.Options.AudioNormalize = &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}
	if _, err = e.ExecutePlan(ctx, combined, filepath.Join(t.TempDir(), "combined.mp4")); err != nil {
		t.Fatal("combined canvas, normalization and replacement", err)
	}
}

func TestRenderMeasuredNormalizationAndLoudnessActualMedia(t *testing.T) {
	_, _ = renderFixture(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "quiet.wav")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=12", "-af", "volume=0.05", source)
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	ctx := context.Background()
	before, err := e.AnalyzeLoudness(ctx, source, LoudnessOptions{})
	if err != nil || before.IntegratedLUFS == nil || *before.IntegratedLUFS > -35 || before.AnalyzedEndMS < 11900 {
		t.Fatalf("source loudness: %+v %v", before, err)
	}
	i, tp, lra := -16.0, -1.0, 11.0
	target := &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}
	for _, joined := range []bool{false, true} {
		output := filepath.Join(t.TempDir(), "normalized.mp3")
		if joined {
			_, err = e.ExecutePlan(ctx, Plan{Version: 1, Input: source, OutputMode: "joined", Ranges: []NamedRange{{StartMS: 8000, EndMS: 12000}, {StartMS: 0, EndMS: 4000}}, Options: AdvancedOptions{Options: Options{Format: "mp3"}, AudioNormalize: target}}, output)
		} else {
			_, err = e.ExportWithOptions(ctx, source, output, Range{EndMS: 8000}, AdvancedOptions{Options: Options{Format: "mp3"}, AudioNormalize: target})
		}
		if err != nil {
			t.Fatal(err)
		}
		// The verifier uses ebur128's independent summary, not the producer's
		// loudnorm JSON parser or its claimed target settings.
		data := string(renderTool(t, "-hide_banner", "-nostats", "-i", output, "-af", "ebur128=peak=true", "-f", "null", "-"))
		at := strings.LastIndex(data, "Summary:")
		if at < 0 {
			t.Fatal("missing independent EBU R128 summary")
		}
		match := regexp.MustCompile(`I:\s+(-?[0-9.]+) LUFS`).FindStringSubmatch(data[at:])
		if len(match) != 2 {
			t.Fatal(data[at:])
		}
		actual, _ := strconv.ParseFloat(match[1], 64)
		peak := regexp.MustCompile(`Peak:\s+(-?[0-9.]+) dBFS`).FindStringSubmatch(data[at:])
		if len(peak) != 2 {
			t.Fatal(data[at:])
		}
		actualPeak, _ := strconv.ParseFloat(peak[1], 64)
		if math.Abs(actual-i) > 1 || actualPeak > tp+.5 {
			t.Fatalf("normalization measured I=%v TP=%v", actual, actualPeak)
		}
	}
	silent := filepath.Join(dir, "silent.wav")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "4", silent)
	report, err := e.AnalyzeLoudness(ctx, silent, LoudnessOptions{})
	if err != nil || report.IntegratedLUFS != nil || report.TruePeakDBTP != nil || report.LoudnessRangeLU != nil {
		t.Fatalf("silent metrics: %+v %v", report, err)
	}
	_, err = e.ExportWithOptions(ctx, silent, filepath.Join(t.TempDir(), "silent.mp3"), Range{EndMS: 4000}, AdvancedOptions{Options: Options{Format: "mp3"}, AudioNormalize: target})
	if err != nil {
		t.Fatal("normalizing silence", err)
	}
}

func TestRenderRejectsIncompatibleOptionsBeforePublication(t *testing.T) {
	video, audio := renderFixture(t)
	e := New(Config{FFmpegThreads: 1})
	i, tp, lra := -16.0, -1.0, 11.0
	canvas := &Canvas{Width: 180, Height: 320, Fit: "contain", Background: "#000000"}
	normalize := &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}
	zero := 0
	cases := []AdvancedOptions{
		{Options: Options{Format: "mp4", CutMode: "copy"}, Canvas: canvas},
		{Options: Options{Format: "mp4", Quality: "720p"}, Canvas: canvas},
		{Options: Options{Format: "mp3"}, Canvas: canvas},
		{Options: Options{Format: "mkv", CutMode: "copy"}, AudioNormalize: normalize},
		{Options: Options{Format: "mp4"}, NoAudio: true, AudioNormalize: normalize},
		{Options: Options{Format: "mp4"}, AudioNormalize: &AudioNormalize{IntegratedLUFS: &i, LoudnessRangeLU: &lra}},
		{Options: Options{Format: "mp4"}, AudioTrack: &zero, AudioReplace: &AudioReplace{Input: audio}},
		{Options: Options{Format: "mp4"}, AudioReplace: &AudioReplace{SourceID: "unbound"}},
		{Options: Options{Format: "mp4"}, AudioReplace: &AudioReplace{Input: audio, OffsetMS: 86400001}},
	}
	for _, options := range cases {
		dir := t.TempDir()
		_, err := e.ExportWithOptions(context.Background(), video, filepath.Join(dir, "result."+options.Format), Range{EndMS: 1000}, options)
		if CodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid render options accepted/misclassified %+v: %v", options, err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("invalid request left outputs", entries)
		}
	}
}

func TestRenderExternalTrackClockAndInputFences(t *testing.T) {
	video, audio := renderFixture(t)
	directory := t.TempDir()
	external := filepath.Join(directory, "offset-multitrack.mka")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1", "-itsoffset", "5", "-i", audio, "-map", "0:a", "-map", "1:a", "-copyts", "-c:a", "pcm_s16le", external)
	// The container's first stream starts at 0; the selected second stream at
	// 5s remains delayed on that container timeline. A replacement offset -4s
	// therefore places its first actual sample at source timestamp 1s.
	track := 1
	e := New(Config{FFmpegThreads: 1})
	out := filepath.Join(t.TempDir(), "track.mp4")
	_, err := e.ExportWithOptions(context.Background(), video, out, Range{EndMS: 2000}, AdvancedOptions{Options: Options{Format: "mp4"}, AudioReplace: &AudioReplace{Input: external, AudioTrack: &track, OffsetMS: -4000}})
	if err != nil {
		t.Fatal(err)
	}
	pcm := renderTool(t, "-v", "error", "-i", out, "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
	if pcmRMS(pcm, .1, .8) > 10 || pcmRMS(pcm, 1.1, 1.8) < 1500 {
		t.Fatal("selected external stream/container start clock was ignored")
	}
	coverage, err := e.AnalyzeLoudness(context.Background(), external, LoudnessOptions{AudioTrack: &track})
	if err != nil || coverage.AnalyzedEndMS < 5900 || coverage.AnalyzedEndMS > 6100 {
		t.Fatalf("delayed stream loudness coverage lost its source clock: %+v %v", coverage, err)
	}
	for _, replacePrimary := range []bool{false, true} {
		source, replacement := renderFixture(t)
		e := New(Config{FFmpegThreads: 1})
		originalPreflight := e.preflight
		e.preflight = func(a, b string) error {
			if err := originalPreflight(a, b); err != nil {
				return err
			}
			path := replacement
			if replacePrimary {
				path = source
			}
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = file.Write([]byte{1})
			_ = file.Close()
			return err
		}
		dir := t.TempDir()
		_, err = e.ExportWithOptions(context.Background(), source, filepath.Join(dir, "fenced.mp4"), Range{EndMS: 1000}, AdvancedOptions{Options: Options{Format: "mp4"}, AudioReplace: &AudioReplace{Input: replacement}})
		if CodeOf(err) != CodeSourceChanged {
			t.Fatalf("changed input not fenced (primary=%v): %v", replacePrimary, err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("source change leaked media", entries)
		}
	}
}

func TestRenderCanvasPreservesKnownHDRAndVariableClocks(t *testing.T) {
	e := New(Config{FFmpegThreads: 1})
	ctx := context.Background()
	for _, name := range []string{"pq-10bit.mp4", "hlg-10bit.mp4", "mono-silence-vfr.mp4"} {
		t.Run(name, func(t *testing.T) {
			input := filepath.Join("..", "..", "internal", "app", "testdata", "media-contract", name)
			output := filepath.Join(t.TempDir(), "canvas.mp4")
			_, err := e.ExecutePlan(ctx, Plan{Version: 1, Input: input, OutputMode: "joined", Ranges: []NamedRange{{EndMS: 2000}}, Options: AdvancedOptions{Options: Options{Format: "mp4"}, Canvas: &Canvas{Width: 128, Height: 160, Fit: "contain", Background: "#000000"}}}, output)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(name, "mono") {
				data, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_frames", "-show_entries", "frame=best_effort_timestamp_time", "-of", "json", output).Output()
				if err != nil {
					t.Fatal(err)
				}
				var p struct {
					Frames []struct {
						PTS string `json:"best_effort_timestamp_time"`
					} `json:"frames"`
				}
				if err = json.Unmarshal(data, &p); err != nil {
					t.Fatal(err)
				}
				want := []float64{0, .04, .2, .32, .6, .8, 1.04, 1.4, 1.96}
				if len(p.Frames) != len(want) {
					t.Fatalf("VFR frame count %d want%d", len(p.Frames), len(want))
				}
				for i, f := range p.Frames {
					v, _ := strconv.ParseFloat(f.PTS, 64)
					if math.Abs(v-want[i]) > .005 {
						t.Fatalf("VFR frame %d PTS%v want%v", i, v, want[i])
					}
				}
				return
			}
			rgb := renderTool(t, "-v", "error", "-i", output, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
			if len(rgb) != 128*160*3 {
				t.Fatal("wrong HDR canvas dimensions")
			}
			last := -1
			levels := make([]int, 7)
			for i := range 7 {
				at := (80*128 + (2*i+1)*128/16) * 3
				pixel := rgb[at : at+3]
				levels[i] = int(pixel[0])
				if math.Abs(float64(pixel[0])-float64(pixel[1])) > 5 || math.Abs(float64(pixel[1])-float64(pixel[2])) > 5 || i == 0 && pixel[0] > 8 || i > 0 && int(pixel[0]) <= last {
					t.Fatalf("HDR signal became tinted/nonmonotonic: %v", levels)
				}
				last = int(pixel[0])
			}
			if levels[4] < 190 || levels[4] > 250 || levels[6] < 230 {
				t.Fatalf("HDR absolute luminance tone map changed: %v", levels)
			}
		})
	}
}

func TestRenderLoudnessWindowSelectionAndActualCoverage(t *testing.T) {
	_, _ = renderFixture(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "window.wav")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "aevalsrc=if(lt(t\\,2)\\,0.01\\,0.2)*sin(2*PI*1000*t):s=48000:d=4", source)
	e := New(Config{FFmpegThreads: 1})
	ctx := context.Background()
	quiet, err := e.AnalyzeLoudness(ctx, source, LoudnessOptions{EndMS: 1000})
	if err != nil || quiet.IntegratedLUFS == nil {
		t.Fatalf("quiet window: %+v %v", quiet, err)
	}
	loud, err := e.AnalyzeLoudness(ctx, source, LoudnessOptions{StartMS: 3000, EndMS: 4000})
	if err != nil || loud.IntegratedLUFS == nil || *loud.IntegratedLUFS-*quiet.IntegratedLUFS < 24 {
		t.Fatalf("window leaked adjacent loudness: %+v %+v %v", quiet, loud, err)
	}
	short := filepath.Join(dir, "short-audio.mp4")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "color=red:size=160x90:rate=25:duration=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", short)
	coverage, err := e.AnalyzeLoudness(ctx, short, LoudnessOptions{})
	if err != nil || coverage.EndMS != 4000 || coverage.AnalyzedEndMS < 1900 || coverage.AnalyzedEndMS > 2100 {
		t.Fatalf("undecoded tail was assumed silent: %+v %v", coverage, err)
	}
	multiple := filepath.Join(dir, "two-audio.mka")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=4", "-filter:a:1", "volume=0.1", "-map", "0:a", "-map", "1:a", "-c:a", "pcm_s16le", multiple)
	first, err := e.AnalyzeLoudness(ctx, multiple, LoudnessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	selected := 1
	second, err := e.AnalyzeLoudness(ctx, multiple, LoudnessOptions{AudioTrack: &selected})
	if err != nil || first.AudioTrack != 0 || second.AudioTrack != 1 || first.IntegratedLUFS == nil || second.IntegratedLUFS == nil || *first.IntegratedLUFS-*second.IntegratedLUFS < 18 {
		t.Fatalf("absolute audio selection ignored: %+v %+v %v", first, second, err)
	}
}
