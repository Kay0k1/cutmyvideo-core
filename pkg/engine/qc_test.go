package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// qcFloatWAV writes mono 48 kHz IEEE float samples. Values above 1.0 survive
// the container, which is how a float mix can clip after encoding.
func qcFloatWAV(t *testing.T, samples []float32) string {
	t.Helper()
	data := make([]byte, 4*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint32(data[4*i:], math.Float32bits(v))
	}
	header := append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(36+len(data)))...)
	header = append(header, "WAVEfmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16)
	header = binary.LittleEndian.AppendUint16(header, 3) // WAVE_FORMAT_IEEE_FLOAT
	header = binary.LittleEndian.AppendUint16(header, 1)
	header = binary.LittleEndian.AppendUint32(header, 48000)
	header = binary.LittleEndian.AppendUint32(header, 48000*4)
	header = binary.LittleEndian.AppendUint16(header, 4)
	header = binary.LittleEndian.AppendUint16(header, 32)
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(data)))
	path := filepath.Join(t.TempDir(), "-oracle.wav")
	if err := os.WriteFile(path, append(header, data...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func qcFailed(report QCReport) string {
	var failed []string
	for _, check := range report.Checks {
		if check.Status != "pass" {
			failed = append(failed, check.Name)
		}
	}
	return strings.Join(failed, ",")
}

func near(got, want, tolerance int64) bool { return got >= want-tolerance && got <= want+tolerance }

func TestQCFrameCadenceDropsAndFastStart(t *testing.T) {
	analysisTools(t)
	dir := t.TempDir()
	steady, fast, dropped := filepath.Join(dir, "steady.mp4"), filepath.Join(dir, "fast.mp4"), filepath.Join(dir, "dropped.mp4")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "testsrc2=s=320x240:r=30:d=3", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:d=3",
		"-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", steady)
	analysisFixtureCommand(t, "-i", steady, "-c", "copy", "-movflags", "+faststart", fast)
	// Remove frames 40..54 and keep every other timestamp, as a broken splice does.
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "testsrc2=s=320x240:r=30:d=3", "-vf", "select='not(between(n,40,54))'", "-fps_mode", "passthrough",
		"-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", dropped)
	e := New(Config{FFmpegThreads: 1})
	ctx := context.Background()

	report, err := e.QC(ctx, steady, QCOptions{Expect: []string{"fps=30", "cfr", "no-drops", "size=320x240", "duration=3", "audio", "video"}})
	if err != nil || !report.Passed || len(report.Checks) != 7 {
		t.Fatalf("steady: %v %+v", err, report.Checks)
	}
	v := report.Video
	if v.Timing != "cfr" || v.FrameCount != 90 || v.ExpectedFrames != 90 || *v.FPS != 30 || v.FirstFrameMS != 0 || v.EndMS != 3000 || v.GapCount != 0 || v.RFrameRate != "30/1" || v.Codec != "h264" || v.PixelFormat != "yuv420p" {
		t.Fatalf("steady video: %+v", v)
	}
	if report.Version != 1 || report.Container.Format == "" || report.Container.SizeBytes <= 0 || len(report.Container.Streams) != 2 || report.Audio == nil || report.Sync == nil || !near(report.Sync.StartOffsetMS, 0, 30) {
		t.Fatalf("steady report: %+v", report)
	}
	if report.Container.FastStart == nil || *report.Container.FastStart {
		t.Fatal("moov after mdat reported as fast start", report.Container)
	}
	faststart, err := e.QC(ctx, fast, QCOptions{Expect: []string{"faststart"}})
	if err != nil || faststart.Container.FastStart == nil || !*faststart.Container.FastStart {
		t.Fatal("fast start not detected", faststart.Container, err)
	}
	if _, err = e.QC(ctx, steady, QCOptions{Expect: []string{"faststart"}}); CodeOf(err) != CodeExpectationFailed {
		t.Fatal("faststart expectation accepted", err)
	}

	report, err = e.QC(ctx, dropped, QCOptions{Expect: []string{"fps=30", "cfr", "no-drops", "no-audio"}})
	if CodeOf(err) != CodeExpectationFailed || !strings.Contains(err.Error(), "no-drops") {
		t.Fatal("dropped frames accepted", err)
	}
	v = report.Video
	if report.Version != 1 || report.Passed || qcFailed(report) != "fps,cfr,no-drops" || v.Timing != "vfr" || v.FrameCount != 75 || v.ExpectedFrames != 90 || v.GapCount != 1 || v.MissingFrames != 15 || len(v.Gaps) != 1 || !near(v.Gaps[0].AtMS, 1333, 1) || v.Gaps[0].MissingFrames != 15 {
		t.Fatalf("dropped report: %+v %+v", v, report.Checks)
	}
	if report.Audio != nil || report.Sync != nil {
		t.Fatal("video-only file reported audio", report)
	}
}

func TestQCVariableRateBlackAndFrozenPictures(t *testing.T) {
	analysisTools(t)
	e := New(Config{FFmpegThreads: 1})
	ctx := context.Background()
	vfr := filepath.Join("..", "..", "internal", "app", "testdata", "media-contract", "mono-silence-vfr.mp4")
	report, err := e.QC(ctx, vfr, QCOptions{Expect: []string{"cfr"}})
	if CodeOf(err) != CodeExpectationFailed || report.Video == nil || report.Video.Timing != "vfr" || report.Video.FrameCount != 9 || report.Video.IntervalMS.Min != 40 || report.Video.IntervalMS.Max != 560 {
		t.Fatalf("variable frame timestamps: %v %+v", err, report.Video)
	}

	// Frames 29..59 repeat one picture; frames 75..89 are black (30 fps).
	pictures := filepath.Join(t.TempDir(), "pictures.mp4")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "testsrc2=s=320x240:r=30:d=4",
		"-filter_complex", "[0:v]split[a][b];[a][b]freezeframes=first=30:last=59:replace=29,drawbox=c=black:t=fill:enable='between(n,75,89)'[v]",
		"-map", "[v]", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", pictures)
	report, err = e.QC(ctx, pictures, QCOptions{Expect: []string{"cfr", "no-black", "no-freeze"}})
	if CodeOf(err) != CodeExpectationFailed || qcFailed(report) != "no-black,no-freeze" {
		t.Fatal("black and frozen pictures accepted", err, report.Checks)
	}
	v := report.Video
	if len(v.Black) != 1 || !near(v.Black[0].StartMS, 2500, 34) || !near(v.Black[0].EndMS, 3000, 34) {
		t.Fatalf("black interval: %+v", v.Black)
	}
	// The black picture is not reported again as a frozen one.
	if len(v.Freeze) != 1 || !near(v.Freeze[0].StartMS, 967, 34) || !near(v.Freeze[0].EndMS, 2000, 34) {
		t.Fatalf("frozen interval: %+v", v.Freeze)
	}
	window, err := e.QC(ctx, pictures, QCOptions{StartMS: 2000, EndMS: 4000})
	if err != nil || window.StartMS != 2000 || window.Video.FrameCount != 60 || window.Video.FirstFrameMS != 2000 || len(window.Video.Freeze) != 0 || len(window.Video.Black) != 1 || !near(window.Video.Black[0].StartMS, 2500, 34) {
		t.Fatalf("window: %v %+v", err, window.Video)
	}
}

func TestQCAudioClippingPeakSilenceAndSync(t *testing.T) {
	analysisTools(t)
	e := New(Config{FFmpegThreads: 1})
	ctx := context.Background()

	// 300 ms of silence, 1.2 s of a -20 dBFS tone, 500 ms of silence.
	var samples []float32
	for i := 0; i < 48000*2; i++ {
		var v float32
		if i >= 14400 && i < 72000 {
			v = float32(0.1 * math.Sin(2*math.Pi*997*float64(i)/48000))
		}
		samples = append(samples, v)
	}
	report, err := e.QC(ctx, qcFloatWAV(t, samples), QCOptions{Expect: []string{"true-peak=-19", "no-clipping", "no-audio"}})
	if CodeOf(err) != CodeExpectationFailed || qcFailed(report) != "no-audio" {
		t.Fatal("audio checks", err, report.Checks)
	}
	a := report.Audio
	if report.Video != nil || report.Sync != nil || a == nil || a.SampleRate != 48000 || a.Channels != 1 || a.DurationMS != 2000 || a.ClippedSamples != 0 {
		t.Fatalf("audio-only report: %+v", report)
	}
	if a.TruePeakDBTP == nil || math.Abs(*a.TruePeakDBTP+20) > 0.3 || a.PeakDBFS == nil || math.Abs(*a.PeakDBFS+20) > 0.1 || a.IntegratedLUFS == nil {
		t.Fatalf("tone peak: %+v", a)
	}
	if !near(a.LeadingSilenceMS, 300, 20) || !near(a.TrailingSilenceMS, 500, 20) {
		t.Fatalf("edge silence: %+v", a)
	}

	// A float mix above full scale: count the oracle's own overs.
	var over int64
	for i := range samples {
		v := float32(1.25 * math.Sin(2*math.Pi*441*float64(i)/48000))
		if i < 24000 {
			v = 0.5 * v
		}
		samples[i] = v
		if math.Abs(float64(v)) > 1 {
			over++
		}
	}
	report, err = e.QC(ctx, qcFloatWAV(t, samples), QCOptions{Expect: []string{"no-clipping", "video"}})
	if CodeOf(err) != CodeExpectationFailed || qcFailed(report) != "no-clipping,video" || over == 0 || report.Audio.ClippedSamples != over || *report.Audio.PeakDBFS < 1.9 {
		t.Fatalf("clipping: %v over=%d %+v %+v", err, over, report.Audio, report.Checks)
	}

	// 3 s of video; the audio starts 200 ms later and lasts 2.5 s.
	shifted := filepath.Join(t.TempDir(), "shifted.mkv")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "testsrc2=s=320x240:r=30:d=3", "-itsoffset", "0.2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:d=2.5",
		"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "pcm_s16le", shifted)
	report, err = e.QC(ctx, shifted, QCOptions{Expect: []string{"fps=30", "cfr", "faststart"}})
	if CodeOf(err) != CodeExpectationFailed || qcFailed(report) != "faststart" || report.Container.FastStart != nil || report.Sync == nil {
		t.Fatal("matroska report", err, report.Checks)
	}
	if s := report.Sync; !near(s.StartOffsetMS, 200, 20) || !near(s.DurationDeltaMS, -500, 20) || !near(s.EndOffsetMS, -300, 20) || !near(report.Audio.StartMS, 200, 20) {
		t.Fatalf("sync: %+v %+v", s, report.Audio)
	}
}

func TestQCRejectsInvalidExpectationsBeforeInspection(t *testing.T) {
	e := New(Config{})
	for _, expect := range [][]string{{"fps"}, {"size=wide"}, {"loudness=-14"}, {"cfr", "cfr"}, {"audio", "no-audio"}, {"true-peak=-1±0.5"}, {"duration=abc"}} {
		if _, err := e.QC(context.Background(), "missing.mp4", QCOptions{Expect: expect}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%q: %v", expect, err)
		}
	}
	if _, err := e.QC(context.Background(), "missing.mp4", QCOptions{MinFreezeMS: 10}); CodeOf(err) != CodeInvalidArgument {
		t.Error("short freeze minimum accepted", err)
	}
	if _, err := e.QC(context.Background(), "missing.mp4", QCOptions{}); CodeOf(err) != CodeInputNotFound {
		t.Error("missing input", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.QC(ctx, "missing.mp4", QCOptions{}); !errors.Is(err, context.Canceled) {
		t.Error("cancellation lost", err)
	}
	analysisTools(t)
	input := analysisWAV(t, 1, 1000, func(int64) bool { return true })
	if _, err := New(Config{ExportTimeout: time.Nanosecond}).QC(context.Background(), input, QCOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Error("whole-operation deadline lost", err)
	}
	if _, err := e.QC(context.Background(), input, QCOptions{StartMS: 500, EndMS: 2000}); CodeOf(err) != CodeInvalidArgument {
		t.Error("window beyond the source accepted", err)
	}
	if report, err := e.QC(context.Background(), input, QCOptions{}); err != nil || !report.Passed || report.Checks == nil || fmt.Sprint(report.Checks) != "[]" {
		t.Error("measurement without expectations", report, err)
	}
}
