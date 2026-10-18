package engine

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func analysisTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
}

// Write the oracle directly as PCM WAV: detection is checked against known
// generated samples, not against a second invocation of the same detector.
func analysisWAV(t *testing.T, channels int, durationMS int64, audible func(int64) bool) string {
	t.Helper()
	const rate = 48000
	frames := durationMS * rate / 1000
	data := make([]byte, frames*int64(channels)*2)
	for frame := int64(0); frame < frames; frame++ {
		var value int16
		if audible(frame * 1000 / rate) {
			value = int16(12000 * math.Sin(2*math.Pi*997*float64(frame)/rate))
		}
		for channel := 0; channel < channels; channel++ {
			sample := value
			if channel%2 == 1 {
				sample = -value
			}
			binary.LittleEndian.PutUint16(data[(frame*int64(channels)+int64(channel))*2:], uint16(sample))
		}
	}
	var header []byte
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(36+len(data)))
	header = append(header, "WAVEfmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16)
	header = binary.LittleEndian.AppendUint16(header, 1)
	header = binary.LittleEndian.AppendUint16(header, uint16(channels))
	header = binary.LittleEndian.AppendUint32(header, rate)
	header = binary.LittleEndian.AppendUint32(header, uint32(rate*channels*2))
	header = binary.LittleEndian.AppendUint16(header, uint16(channels*2))
	header = binary.LittleEndian.AppendUint16(header, 16)
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(data)))
	path := filepath.Join(t.TempDir(), "-analysis.wav")
	if err := os.WriteFile(path, append(header, data...), 0600); err != nil {
		t.Fatal(err)
	}
	// A valid RIFF/WAVE header is independent of the decoder used below.
	if len(header) != 44 || string(header[8:16]) != "WAVEfmt " {
		t.Fatal("invalid fixture header", header)
	}
	return path
}

func analysisFixtureCommand(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture failed: %v %s", err, data)
	}
}

func TestAnalysisSilenceActualAntiphaseTimelineAndExecutablePlan(t *testing.T) {
	analysisTools(t)
	input := analysisWAV(t, 2, 4000, func(ms int64) bool { return ms >= 500 && ms < 1500 || ms >= 2500 && ms < 3500 })
	before, _ := os.ReadFile(input)
	e := New(Config{FFmpegThreads: 1})
	result, err := e.AnalyzeSilence(context.Background(), input, SilenceOptions{ThresholdDB: -40, MinSilenceMS: 200, KeepBeforeMS: 100, KeepAfterMS: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || result.AudioTrack != 0 || result.AnalyzedEndMS != 4000 || fmt.Sprint(result.Silence) != "[{0 500} {1500 2500} {3500 4000}]" || fmt.Sprint(result.Keep) != "[{400 1600 } {2400 3600 }]" {
		t.Fatalf("actual PCM timeline: %+v", result)
	}
	plan, err := BuildSilencePlan(input, result, AdvancedOptions{Options: Options{Format: "mp3"}}, "joined")
	if err != nil || len(plan.Ranges) != 2 || plan.Options.AudioTrack == nil || *plan.Options.AudioTrack != 0 {
		t.Fatal(plan, err)
	}
	// The proposal stays editable: preserve a little more of the second phrase.
	plan.Ranges[1].StartMS = 2300
	output := filepath.Join(t.TempDir(), "edited.mp3")
	exported, err := e.ExecutePlan(context.Background(), plan, output)
	if err != nil || len(exported.Outputs) != 1 {
		t.Fatal(exported, err)
	}
	info, err := e.Inspect(context.Background(), output)
	if err != nil || info.DurationMS < 2450 || info.DurationMS > 2650 {
		t.Fatal("plan did not export reviewed intervals", info, err)
	}
	window, err := e.AnalyzeSilence(context.Background(), input, SilenceOptions{StartMS: 1000, EndMS: 3000, ThresholdDB: -40, MinSilenceMS: 200})
	if err != nil || fmt.Sprint(window.Silence) != "[{1500 2500}]" || window.AnalyzedEndMS != 3000 {
		t.Fatal("nonzero original source offset lost", window, err)
	}
	after, _ := os.ReadFile(input)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("analysis changed source bytes")
	}
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), `"analyzed_end_ms":4000`) {
		t.Fatal("analysis JSON contract lost", string(encoded), err)
	}
}

func TestAnalysisSilenceAllQuietAllAudibleAndExplicitTrack(t *testing.T) {
	analysisTools(t)
	quiet := analysisWAV(t, 1, 2000, func(int64) bool { return false })
	voice := analysisWAV(t, 1, 2000, func(int64) bool { return true })
	e := New(Config{FFmpegThreads: 1})
	for _, tc := range []struct {
		path  string
		quiet bool
	}{{quiet, true}, {voice, false}} {
		result, err := e.AnalyzeSilence(context.Background(), tc.path, SilenceOptions{KeepBeforeMS: 100, KeepAfterMS: 100})
		if err != nil {
			t.Fatal(tc.quiet, err)
		}
		if tc.quiet && (len(result.Silence) != 1 || len(result.Keep) != 0) || !tc.quiet && (len(result.Silence) != 0 || len(result.Keep) != 1) {
			t.Fatal("silence/no silence mismatch", result)
		}
		if tc.quiet {
			if _, err := BuildSilencePlan(tc.path, result, AdvancedOptions{}, ""); CodeOf(err) != CodeInvalidArgument {
				t.Fatal("all quiet plan was accepted", err)
			}
		}
	}
	multi := filepath.Join(t.TempDir(), "tracks.mkv")
	analysisFixtureCommand(t, "-i", quiet, "-i", voice, "-map", "0:a:0", "-map", "1:a:0", "-c:a", "pcm_s16le", multi)
	result, err := e.AnalyzeSilence(context.Background(), multi, SilenceOptions{AudioTrack: new(1)})
	if err != nil || result.AudioTrack != 1 || len(result.Silence) != 0 || len(result.Keep) != 1 {
		t.Fatal("absolute audio selection ignored", result, err)
	}
	result, err = e.AnalyzeSilence(context.Background(), multi, SilenceOptions{})
	if err != nil || result.AudioTrack != 0 || len(result.Keep) != 0 {
		t.Fatal("default audio selection changed", result, err)
	}
	if _, err := e.AnalyzeSilence(context.Background(), multi, SilenceOptions{AudioTrack: new(2)}); CodeOf(err) != CodeAudioTrackUnavailable {
		t.Fatal("missing audio index not classified", err)
	}
}

func TestAnalysisSilenceReportsUncoveredVideoTail(t *testing.T) {
	analysisTools(t)
	audio := analysisWAV(t, 1, 2000, func(int64) bool { return true })
	input := filepath.Join(t.TempDir(), "short-audio.mkv")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "color=white:s=160x90:r=25:d=4", "-i", audio, "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-threads", "1", "-c:a", "pcm_s16le", input)
	result, err := New(Config{}).AnalyzeSilence(context.Background(), input, SilenceOptions{})
	if err != nil || result.EndMS != 4000 || result.AnalyzedEndMS != 2000 || len(result.Keep) != 1 || result.Keep[0].EndMS != 2000 {
		t.Fatal("uncovered tail was fabricated", result, err)
	}
	if _, err := BuildSilencePlan(input, result, AdvancedOptions{}, "joined"); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("uncovered video tail was silently dropped", err)
	}
}

func TestAnalysisVideoActualSceneAndBlackTimeline(t *testing.T) {
	analysisTools(t)
	input := filepath.Join(t.TempDir(), "scenes.mp4")
	analysisFixtureCommand(t,
		"-f", "lavfi", "-i", "color=black:s=160x90:r=25:d=1",
		"-f", "lavfi", "-i", "color=white:s=160x90:r=25:d=1",
		"-f", "lavfi", "-i", "color=red:s=160x90:r=25:d=1",
		"-f", "lavfi", "-i", "color=black:s=160x90:r=25:d=1",
		"-filter_complex", "[0:v][1:v][2:v][3:v]concat=n=4:v=1:a=0[v]", "-map", "[v]", "-c:v", "libx264", "-threads", "1", input)
	e := New(Config{FFmpegThreads: 1})
	result, err := e.AnalyzeVideo(context.Background(), input, VideoAnalysisOptions{SceneThreshold: 0.1, MinBlackMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	if result.EndMS != 4000 || result.AnalyzedEndMS != 3960 || result.VideoTrack != 0 || len(result.Scenes) != 3 || fmt.Sprint(result.Black) != "[{0 1000} {3000 3960}]" {
		t.Fatalf("actual known video timeline: %+v", result)
	}
	for i, scene := range result.Scenes {
		if scene.AtMS != int64(i+1)*1000 || scene.Score < 0.1 || scene.Score > 1 {
			t.Fatal("scene boundary mismatch", result)
		}
	}
	window, err := e.AnalyzeVideo(context.Background(), input, VideoAnalysisOptions{StartMS: 500, EndMS: 2500, SceneThreshold: 0.1, MinBlackMS: 200})
	if err != nil || len(window.Scenes) != 2 || window.Scenes[0].AtMS != 1000 || window.Scenes[1].AtMS != 2000 || len(window.Black) != 1 || window.Black[0].StartMS < 500 || window.Black[0].EndMS != 1000 {
		t.Fatal("visual window offset mismatch", window, err)
	}
	if _, err := e.AnalyzeSilence(context.Background(), input, SilenceOptions{}); CodeOf(err) != CodeAudioMissing {
		t.Fatal("missing audio not classified", err)
	}
	audio := analysisWAV(t, 1, 1000, func(int64) bool { return true })
	if _, err := e.AnalyzeVideo(context.Background(), audio, VideoAnalysisOptions{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("audio-only scene analysis accepted", err)
	}
}

func TestAnalysisPlanBoundsAndSelectedTrack(t *testing.T) {
	valid := SilenceResult{Version: 1, StartMS: 0, EndMS: 1000, AnalyzedEndMS: 1000, AudioTrack: 2, Keep: []AnalysisRange{{StartMS: 100, EndMS: 900}}}
	plan, err := BuildSilencePlan("source.mp4", valid, AdvancedOptions{}, "")
	if err != nil || len(plan.Ranges) != 1 || plan.Ranges[0].Label != "speech-001" || plan.OutputMode != "separate" || *plan.Options.AudioTrack != 2 {
		t.Fatal(plan, err)
	}
	for _, mutate := range []func(*SilenceResult){
		func(r *SilenceResult) { r.Version = 2 },
		func(r *SilenceResult) { r.AnalyzedEndMS = 900 },
		func(r *SilenceResult) { r.Keep = nil },
		func(r *SilenceResult) { r.Keep = make([]AnalysisRange, 129) },
		func(r *SilenceResult) { r.Keep = []AnalysisRange{{StartMS: 900, EndMS: 100}, {StartMS: 1, EndMS: 2}} },
		func(r *SilenceResult) { r.Keep = []AnalysisRange{{StartMS: -1, EndMS: 200}} },
		func(r *SilenceResult) { r.Keep = []AnalysisRange{{StartMS: 0, EndMS: 1001}} },
	} {
		result := valid
		mutate(&result)
		if _, err := BuildSilencePlan("source.mp4", result, AdvancedOptions{}, "joined"); CodeOf(err) != CodeInvalidArgument {
			t.Fatal("invalid plan accepted", result, err)
		}
	}
	for _, options := range []AdvancedOptions{{AudioTrack: new(3)}, {AudioTrack: new(2), NoAudio: true}} {
		if _, err := BuildSilencePlan("source.mp4", valid, options, "joined"); CodeOf(err) != CodeInvalidArgument {
			t.Fatal("conflicting plan audio accepted", err)
		}
	}
	if plan, err := BuildSilencePlan("source.mp4", valid, AdvancedOptions{NoAudio: true}, "joined"); err != nil || plan.Options.AudioTrack != nil || !plan.Options.NoAudio {
		t.Fatal("explicit silent video not supported", plan, err)
	}
	for name, options := range map[string]AdvancedOptions{
		"replacement with defaults": {AudioReplace: &AudioReplace{Input: "recorder.wav"}},
		"replacement with explicit stream": {Options: Options{Format: "mp4", Quality: "best", CutMode: "accurate"},
			AudioReplace: &AudioReplace{Input: "recorder.wav", AudioTrack: new(1), OffsetMS: -100}},
		"replacement with normalization and canvas": {AudioReplace: &AudioReplace{Input: "recorder.wav", AudioTrack: new(0), OffsetMS: 100},
			AudioNormalize: &AudioNormalize{IntegratedLUFS: new(-16.0), TruePeakDBTP: new(-1.0), LoudnessRangeLU: new(11.0)},
			Canvas:         &Canvas{Width: 320, Height: 240, Fit: "contain", Background: "#000000"}},
	} {
		t.Run(name, func(t *testing.T) {
			before, err := json.Marshal(options)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := BuildSilencePlan("source.mp4", valid, options, "joined")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Plan
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			_, _, request, err := planRequest(decoded, "export.mp4")
			if err != nil {
				t.Fatal(err)
			}
			if err := request.Validate(localmedia.Config{MaxRanges: 128, MaxRangeMS: 1000, MaxJobMS: 1000}, localmedia.Source{Path: "source.mp4", DurationMS: 1000}); err != nil {
				t.Fatalf("generated replacement plan cannot execute after JSON roundtrip: %v; plan=%s", err, encoded)
			}
			after, err := json.Marshal(options)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Options.AudioTrack != nil || decoded.Options.AudioReplace == nil || string(before) != string(after) {
				t.Fatal("replacement selection changed", decoded.Options, options)
			}
			expected, _ := json.Marshal(options.AudioReplace)
			actual, _ := json.Marshal(decoded.Options.AudioReplace)
			if string(actual) != string(expected) {
				t.Fatalf("replacement changed across plan JSON: got %s, want %s", actual, expected)
			}
		})
	}
	for name, mutate := range map[string]func(*AdvancedOptions){
		"explicit analyzed original stream":  func(o *AdvancedOptions) { o.AudioTrack = new(2) },
		"explicit different original stream": func(o *AdvancedOptions) { o.AudioTrack = new(3) },
		"no audio":                           func(o *AdvancedOptions) { o.NoAudio = true },
		"HTTP reference":                     func(o *AdvancedOptions) { o.AudioReplace = &AudioReplace{SourceID: "http-source"} },
		"both references":                    func(o *AdvancedOptions) { o.AudioReplace.SourceID = "http-source" },
		"empty reference":                    func(o *AdvancedOptions) { o.AudioReplace.Input = "" },
		"blank reference":                    func(o *AdvancedOptions) { o.AudioReplace.Input = " \t" },
		"negative replacement stream":        func(o *AdvancedOptions) { o.AudioReplace.AudioTrack = new(-1) },
		"offset too early":                   func(o *AdvancedOptions) { o.AudioReplace.OffsetMS = -86400001 },
		"offset too late":                    func(o *AdvancedOptions) { o.AudioReplace.OffsetMS = 86400001 },
		"copy mode":                          func(o *AdvancedOptions) { o.CutMode = "copy" },
		"MKV":                                func(o *AdvancedOptions) { o.Format = "mkv" },
		"subtitle preservation":              func(o *AdvancedOptions) { o.SubtitleMode = "preserve" },
	} {
		t.Run(name, func(t *testing.T) {
			options := AdvancedOptions{AudioReplace: &AudioReplace{Input: "recorder.wav"}}
			mutate(&options)
			if _, err := BuildSilencePlan("source.mp4", valid, options, "joined"); CodeOf(err) != CodeInvalidArgument {
				t.Fatalf("invalid replacement plan accepted: %+v; %v", options, err)
			}
		})
	}
}

func TestAnalysisDetectsSourceMutationDuringDecode(t *testing.T) {
	analysisTools(t)
	for _, replace := range []bool{false, true} {
		input := analysisWAV(t, 1, 1000, func(int64) bool { return true })
		ctx := localmedia.WithProcessObserver(context.Background(), func(_ context.Context, tool string, _ time.Duration, processErr error) {
			if tool != "ffmpeg" || processErr != nil {
				return
			}
			if replace {
				data, err := os.ReadFile(input)
				if err != nil {
					t.Error(err)
					return
				}
				path := input + ".replacement"
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Error(err)
					return
				}
				if err := os.Rename(path, input); err != nil {
					t.Error(err)
				}
			} else {
				stamp := time.Now().Add(time.Hour)
				if err := os.Chtimes(input, stamp, stamp); err != nil {
					t.Error(err)
				}
			}
		})
		if _, err := New(Config{}).AnalyzeSilence(ctx, input, SilenceOptions{}); CodeOf(err) != CodeSourceChanged {
			t.Fatal("source mutation accepted", replace, err)
		}
	}
}

func TestAnalysisEngineInvalidInputCancellationAndDeadlines(t *testing.T) {
	e := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.AnalyzeSilence(ctx, "missing", SilenceOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, err := e.AnalyzeVideo(ctx, "missing", VideoAnalysisOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, err := e.AnalyzeSilence(context.Background(), "missing", SilenceOptions{}); CodeOf(err) != CodeInputNotFound {
		t.Fatal("input error lost", err)
	}
	if _, err := e.AnalyzeSilence(context.Background(), "", SilenceOptions{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("empty input accepted", err)
	}
	if _, err := New(Config{MaxOutputBytes: -1}).AnalyzeVideo(context.Background(), "missing", VideoAnalysisOptions{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("invalid config accepted", err)
	}
	analysisTools(t)
	input := analysisWAV(t, 1, 1000, func(int64) bool { return true })
	invalid := filepath.Join(t.TempDir(), "invalid.wav")
	if err := os.WriteFile(invalid, []byte("not a media container"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AnalyzeSilence(context.Background(), invalid, SilenceOptions{}); CodeOf(err) != CodeUnsupportedMedia {
		t.Fatal("invalid finite media not classified", err)
	}
	if _, err := New(Config{InspectTimeout: time.Nanosecond}).AnalyzeSilence(context.Background(), input, SilenceOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("inspection deadline lost", err)
	}
	if _, err := New(Config{ExportTimeout: time.Nanosecond}).AnalyzeVideo(context.Background(), input, VideoAnalysisOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("whole-operation deadline lost", err)
	}
}
