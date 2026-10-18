package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func audio16Fixture(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	path := filepath.Join(t.TempDir(), "stereo24.wav")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "aevalsrc=0.15*sin(2*PI*431*t)|0.12*sin(2*PI*877*t):s=44100:d=3", "-c:a", "pcm_s24le", path)
	return path
}
func pcm24(t *testing.T, path string) []byte {
	t.Helper()
	return renderTool(t, "-v", "error", "-i", path, "-vn", "-c:a", "pcm_s24le", "-f", "s24le", "-")
}

func TestLosslessAudioProfilesAndOrderedSamplesActualMedia(t *testing.T) {
	source := audio16Fixture(t)
	original := pcm24(t, source)
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	for _, format := range []string{"wav", "flac"} {
		t.Run(format, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "trim."+format)
			_, err := e.Export(context.Background(), source, out, Range{StartMS: 250, EndMS: 1250}, Options{Format: format})
			if err != nil {
				t.Fatal(err)
			}
			want := original[11025*6 : 55125*6]
			got := pcm24(t, out)
			if !bytes.Equal(got, want) {
				t.Fatalf("24-bit trim changed original samples: got %d bytes want %d", len(got), len(want))
			}
			joined := filepath.Join(t.TempDir(), "ordered."+format)
			ranges := []NamedRange{{StartMS: 1000, EndMS: 1500}, {EndMS: 500}, {StartMS: 1000, EndMS: 1500}}
			result, err := e.ExecutePlan(context.Background(), Plan{Version: 1, Input: source, OutputMode: "joined", Ranges: ranges, Options: AdvancedOptions{Options: Options{Format: format}}}, joined)
			if err != nil {
				t.Fatal(err)
			}
			want = append(append(append([]byte{}, original[44100*6:66150*6]...), original[:22050*6]...), original[44100*6:66150*6]...)
			if got = pcm24(t, joined); !bytes.Equal(got, want) {
				first := 0
				for first < len(got) && first < len(want) && got[first] == want[first] {
					first++
				}
				t.Fatalf("ordered/repeated lossless samples changed: got %d bytes want %d, first mismatch sample%d (time%.6f)", len(got), len(want), first/6, float64(first/6)/44100)
			}
			if result.Outputs[0].Segments[2].OutputEndMS != 1500 {
				t.Fatal(result)
			}
		})
	}
	video, _ := renderFixture(t)
	replaced := filepath.Join(t.TempDir(), "replacement.wav")
	_, err := e.ExportWithOptions(context.Background(), video, replaced, Range{EndMS: 4000}, AdvancedOptions{Options: Options{Format: "wav"}, AudioReplace: &AudioReplace{Input: source, OffsetMS: 1000}})
	if err != nil {
		t.Fatal("lossless external audio", err)
	}
	want := append(make([]byte, 44100*6), original...)
	if got := pcm24(t, replaced); !bytes.Equal(got, want) {
		t.Fatalf("lossless replacement offset/padding changed samples: got%d want%d", len(got), len(want))
	}
	i, tp, lra := -16.0, -1.0, 11.0
	normalized := filepath.Join(t.TempDir(), "normalized.flac")
	_, err = e.ExportWithOptions(context.Background(), source, normalized, Range{EndMS: 3000}, AdvancedOptions{Options: Options{Format: "flac"}, AudioNormalize: &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}})
	if err != nil {
		t.Fatal("lossless normalization", err)
	}
	measured, err := e.AnalyzeLoudness(context.Background(), normalized, LoudnessOptions{})
	if err != nil || measured.IntegratedLUFS == nil || math.Abs(*measured.IntegratedLUFS+16) > .6 {
		t.Fatalf("lossless normalized delivery: %+v %v", measured, err)
	}
}

func TestSpeedPitchReplacementOrderAndSourceMapsActualMedia(t *testing.T) {
	video, audio := renderFixture(t)
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	out := filepath.Join(t.TempDir(), "fast.mp4")
	plan := Plan{Version: 1, Input: video, OutputMode: "joined", Ranges: []NamedRange{{EndMS: 2000}, {StartMS: 3000, EndMS: 4000}, {StartMS: 1000, EndMS: 2000}}, Options: AdvancedOptions{Options: Options{Format: "mp4"}, Speed: &Speed{Numerator: 2, Denominator: 1}, AudioReplace: &AudioReplace{Input: audio, OffsetMS: 1000}}}
	i, tp, lra := -16.0, -1.0, 11.0
	plan.Options.AudioNormalize = &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}
	result, err := e.ExecutePlan(context.Background(), plan, out)
	if err != nil {
		t.Fatal(err)
	}
	segs := result.Outputs[0].Segments
	if len(segs) != 3 || segs[0].SourceEndMS != 2000 || segs[0].OutputEndMS != 1000 || segs[1].OutputEndMS != 1500 || segs[2].OutputEndMS != 2000 {
		t.Fatal(segs)
	}
	pcm := renderTool(t, "-v", "error", "-i", out, "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
	for _, w := range []struct {
		a, b float64
		tone bool
	}{{.1, .4, false}, {.6, .9, true}, {1.1, 1.4, false}, {1.6, 1.9, true}} {
		rms := pcmRMS(pcm, w.a, w.b)
		if rms < 0 || w.tone && rms < 1000 || !w.tone && rms > 15 {
			t.Fatalf("source offset→speed wrong at %+v: RMS%v", w, rms)
		}
	}
	crossings := 0
	for i := int(.65 * 48000); i < int(.85*48000); i++ {
		a := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		b := int16(binary.LittleEndian.Uint16(pcm[(i+1)*2:]))
		if a <= 0 && b > 0 {
			crossings++
		}
	}
	if math.Abs(float64(crossings)/.2-880) > 20 {
		t.Fatalf("speed changed pitch: %vHz", float64(crossings)/.2)
	}
	slow := filepath.Join(t.TempDir(), "slow.mp3")
	single, err := e.ExportWithOptions(context.Background(), video, slow, Range{StartMS: 1000, EndMS: 2000}, AdvancedOptions{Options: Options{Format: "mp3"}, Speed: &Speed{Numerator: 1, Denominator: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if single.ActualStartMS != 1000 || single.ActualEndMS != 2000 || single.Segments[0].OutputEndMS != 2000 {
		t.Fatal(single)
	}
	info, err := e.Inspect(context.Background(), slow)
	if err != nil || math.Abs(float64(info.DurationMS-2000)) > 100 {
		t.Fatal(info, err)
	}
}

func TestTargetSizeTwoPassCompleteOutputActualMedia(t *testing.T) {
	video := filepath.Join(t.TempDir(), "motion.mp4")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=480x270:rate=30:duration=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-c:v", "libx264", "-threads", "1", "-crf", "16", "-c:a", "aac", video)
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	out := filepath.Join(t.TempDir(), "sized.mp4")
	i, tp, lra := -16.0, -1.0, 11.0
	result, err := e.ExportWithOptions(context.Background(), video, out, Range{EndMS: 4000}, AdvancedOptions{Options: Options{Format: "mp4"}, Speed: &Speed{Numerator: 5, Denominator: 4}, TargetSizeBytes: 220000, AudioNormalize: &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.Inspect(context.Background(), out)
	if err != nil || result.SizeBytes > 220000 || info.Width != 480 || info.Height != 270 || math.Abs(float64(info.DurationMS-3200)) > 100 {
		t.Fatalf("size/full timeline/geometry: %+v %+v %v", result, info, err)
	}
	control, err := e.ExportWithOptions(context.Background(), video, filepath.Join(t.TempDir(), "unbounded.mp4"), Range{EndMS: 4000}, AdvancedOptions{Options: Options{Format: "mp4"}, Speed: &Speed{Numerator: 5, Denominator: 4}, AudioNormalize: &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}})
	if err != nil || control.SizeBytes <= 220000 {
		t.Fatalf("motion fixture does not exercise actual bitrate reduction: %+v %v", control, err)
	}
	pcm := renderTool(t, "-v", "error", "-i", out, "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
	if pcmRMS(pcm, .2, 2.8) < 1000 {
		t.Fatal("target size silently removed audio")
	}
	impossible := filepath.Join(t.TempDir(), "impossible.mp4")
	_, err = e.ExportWithOptions(context.Background(), video, impossible, Range{EndMS: 4000}, AdvancedOptions{Options: Options{Format: "mp4"}, TargetSizeBytes: 10000})
	if CodeOf(err) != CodeTargetSizeUnattainable {
		t.Fatalf("impossible target code %s: %v", CodeOf(err), err)
	}
	if _, err = os.Stat(impossible); !os.IsNotExist(err) {
		t.Fatal("impossible target published", err)
	}
	// A long audio stream must not hide truncated/missing video behind a
	// superficially correct container duration (including the frame-cap case).
	shortVideo := filepath.Join(t.TempDir(), "short-video.mp4")
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "color=red:size=160x90:rate=25:duration=0.8", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", shortVideo)
	missing := filepath.Join(t.TempDir(), "incomplete-video.mp4")
	_, err = e.ExportWithOptions(context.Background(), shortVideo, missing, Range{EndMS: 3000}, AdvancedOptions{Options: Options{Format: "mp4"}, TargetSizeBytes: 250000})
	if CodeOf(err) != CodeTargetSizeUnattainable {
		t.Fatalf("audio duration concealed truncated video: %s %v", CodeOf(err), err)
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("incomplete video published", err)
	}
}

func TestTargetSizeCancelledPassLeavesNoOutputActualMedia(t *testing.T) {
	video, _ := renderFixture(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "cancelled.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	passes := 0
	ctx = localmedia.WithProcessObserver(ctx, func(_ context.Context, path string, _ time.Duration, _ error) {
		if strings.Contains(filepath.Base(path), "ffmpeg") {
			passes++
			cancel()
		}
	})
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	_, err := e.ExportWithOptions(ctx, video, out, Range{EndMS: 4000}, AdvancedOptions{Options: Options{Format: "mp4"}, TargetSizeBytes: 250000})
	if CodeOf(err) != CodeCancelled || passes != 1 {
		t.Fatalf("cancelled pass outcome %s %v, passes%d", CodeOf(err), err, passes)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled encoding leaked output/passlogs: %v %v", entries, err)
	}
}

type audio16Packet struct {
	PTS  string `json:"pts_time"`
	Hash string `json:"data_hash"`
}

func audio16Packets(t *testing.T, path string) []audio16Packet {
	t.Helper()
	raw, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=pts_time,data_hash", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Packets []audio16Packet `json:"packets"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data.Packets
}

func audio16CopyFixture(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
	path := filepath.Join(t.TempDir(), "deterministic-aac.mp4")
	// AAC perceptual noise substitution depends on decoder history. Starting
	// the same unchanged packets midstream can therefore differ by one PCM LSB.
	// Disable PNS only in this fixture so the exact sample/priming oracle below
	// detects a real shifted boundary independently of that decoder state.
	renderTool(t, "-v", "error", "-f", "lavfi", "-i", "color=red:size=160x90:rate=25:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4",
		"-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-aac_pns", "0", path)
	return path
}

func TestAACM4APacketCopyAndReorderedJoinActualMedia(t *testing.T) {
	video := audio16CopyFixture(t)
	source := audio16Packets(t, video)
	known := map[string]float64{}
	for _, p := range source {
		pts, _ := strconv.ParseFloat(p.PTS, 64)
		known[p.Hash] = pts
	}
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	out := filepath.Join(t.TempDir(), "audio.m4a")
	result, err := e.Export(context.Background(), video, out, Range{StartMS: 1250, EndMS: 2750}, Options{Format: "m4a", CutMode: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	copied := audio16Packets(t, out)
	if len(copied) < 50 {
		t.Fatal("missing copied audio packets")
	}
	for _, p := range copied {
		if _, ok := known[p.Hash]; !ok {
			t.Fatal("M4A reencoded/changedAACpacket")
		}
	}
	first := int64(math.Round(known[copied[0].Hash] * 1000))
	if result.ActualStartMS != first || first > 1250 || 1250-first > 30 {
		t.Fatal("wrong actual packet start", result, first)
	}

	sourcePCM := renderTool(t, "-v", "error", "-i", video, "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
	copyPCM := renderTool(t, "-v", "error", "-i", out, "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
	firstSample := int(math.Round(known[copied[0].Hash] * 48000))
	// One AAC frame needs its previous MDCT overlap. Beyond that startup frame,
	// packet copy must decode to the same original samples, without added priming.
	if len(copyPCM) < 48000*2 || !bytes.Equal(copyPCM[4096*2:48000*2], sourcePCM[(firstSample+4096)*2:(firstSample+48000)*2]) {
		t.Fatal("AAC copy changed decoded samples/priming after the first frame")
	}
	rounded := filepath.Join(t.TempDir(), "rounded.m4a")
	second, err := e.Export(context.Background(), video, rounded, Range{StartMS: 1270, EndMS: 2750}, Options{Format: "m4a", CutMode: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	packets := audio16Packets(t, rounded)
	if second.ActualStartMS != int64(math.Round(known[packets[0].Hash]*1000)) {
		t.Fatal("rounded AAC seek discarded the reported first packet", second)
	}
	joined := filepath.Join(t.TempDir(), "joined.m4a")
	_, err = e.ExecutePlan(context.Background(), Plan{Version: 1, Input: video, OutputMode: "joined", Ranges: []NamedRange{{StartMS: 2000, EndMS: 2500}, {StartMS: 500, EndMS: 1000}, {StartMS: 2000, EndMS: 2500}}, Options: AdvancedOptions{Options: Options{Format: "m4a", CutMode: "copy"}}}, joined)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range audio16Packets(t, joined) {
		if _, ok := known[p.Hash]; !ok {
			t.Fatal("joinedAACpacket changed")
		}
	}
	pcm := audio16Fixture(t)
	_, err = e.Export(context.Background(), pcm, filepath.Join(t.TempDir(), "no.m4a"), Range{EndMS: 1000}, Options{Format: "m4a", CutMode: "copy"})
	if CodeOf(err) != CodeCopyAudioUnsupported {
		t.Fatal("incompatible audio silently reencoded", err)
	}
}
