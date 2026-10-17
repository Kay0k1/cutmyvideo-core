package mediaexport

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type exportRuntimeFixture struct {
	events          []string
	selection       Selection
	selectErr       error
	keyframe        Keyframe
	keyframeErr     error
	keyframeInput   Input
	keyframeStart   int64
	keyframeStream  int
	args            []string
	totalMS         int64
	runErr          error
	onRun           func(context.Context, func(int64)) error
	runHadReport    bool
	size            int64
	statErr         error
	output          mediainfo.Info
	duration        int64
	probeErr        error
	probeRemote     bool
	copyErr         error
	copyCause       error
	copyFormat      string
	renderedErr     error
	renderedCause   error
	hdr             bool
	selectedContext context.Context
	runContext      context.Context
	probeContext    context.Context
}

func newExportRuntimeFixture() *exportRuntimeFixture {
	video := &mediainfo.Stream{Index: 7, CodecType: "video", CodecName: "h264", Width: 160, Height: 90, SampleAspectRatio: "1:1", TimeBase: "1/12800"}
	audio := &mediainfo.Stream{Index: 11, CodecType: "audio", CodecName: "aac", Channels: 2, SampleRate: "48000", TimeBase: "1/48000"}
	audio.Tags.Language, audio.Tags.Title = "rus", "Chosen track"
	output := mediainfo.Info{Streams: []mediainfo.Stream{*video, *audio}}
	output.Format.Duration = "2.000"
	return &exportRuntimeFixture{selection: Selection{Video: video, Audio: audio}, size: 1024, output: output, duration: 2000}
}

func (f *exportRuntimeFixture) SelectInputs(ctx context.Context, _ []Input, _ edit.ExportRequest) (Selection, error) {
	f.events = append(f.events, "select")
	f.selectedContext = ctx
	return f.selection, f.selectErr
}
func (f *exportRuntimeFixture) NearestKeyframe(_ context.Context, path string, start int64, remote bool, stream int) (Keyframe, error) {
	f.events = append(f.events, "keyframe")
	f.keyframeInput, f.keyframeStart, f.keyframeStream = Input{Path: path, Remote: remote}, start, stream
	return f.keyframe, f.keyframeErr
}
func (f *exportRuntimeFixture) Run(ctx context.Context, args []string, totalMS int64, report func(int64)) error {
	f.events = append(f.events, "run")
	f.runContext, f.args, f.totalMS, f.runHadReport = ctx, append([]string(nil), args...), totalMS, report != nil
	if f.onRun != nil {
		return f.onRun(ctx, report)
	}
	return f.runErr
}
func (f *exportRuntimeFixture) StatSize(string) (int64, error) {
	f.events = append(f.events, "stat")
	return f.size, f.statErr
}
func (f *exportRuntimeFixture) Probe(ctx context.Context, _ string, remote bool) (mediainfo.Info, int64, error) {
	f.events = append(f.events, "probe")
	f.probeContext, f.probeRemote = ctx, remote
	return f.output, f.duration, f.probeErr
}
func (f *exportRuntimeFixture) CopyDiagnostic(err error, format string) error {
	f.events = append(f.events, "copy")
	f.copyCause, f.copyFormat = err, format
	if f.copyErr != nil {
		return f.copyErr
	}
	return err
}
func (f *exportRuntimeFixture) RenderedFailure(err error, hdr bool) error {
	f.events = append(f.events, "rendered")
	f.renderedCause, f.hdr = err, hdr
	if f.renderedErr != nil {
		return f.renderedErr
	}
	return err
}

func exportFixtureConfig() Config { return Config{MaxOutputBytes: 64 << 20} }
func exportFixtureRequest() edit.ExportRequest {
	return edit.ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate"}
}
func exportArgsContain(args []string, sequence ...string) bool {
	for i := 0; i+len(sequence) <= len(args); i++ {
		if reflect.DeepEqual(args[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
func exportArgValues(args []string, key string) []string {
	var values []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			values = append(values, args[i+1])
		}
	}
	return values
}

func TestSingleExportPreservesCopyClockAndVerificationOrder(t *testing.T) {
	for _, tc := range []struct {
		name          string
		keyframe      Keyframe
		start, length int64
		seeks         []string
		trim, shift   string
	}{
		{"DTS preroll", Keyframe{PTS: 2000, DTS: 1920, HasDTS: true}, 12000, 2500, []string{"1.920", "2.920", "0"}, "2.580", "-0.080"},
		{"initial missing DTS", Keyframe{PTS: 40}, 10040, 4460, []string{"0.000", "1.000"}, "4.500", "-0.040"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExportRuntimeFixture()
			f.selection.AudioInput, f.keyframe, f.duration = 1, tc.keyframe, tc.length
			var reported []int64
			f.onRun = func(_ context.Context, report func(int64)) error { report(123); return nil }
			request := exportFixtureRequest()
			request.CutMode = "copy"
			inputs := []Input{{Path: "guarded-video", Remote: true, OffsetMS: 10000}, {Path: "local-audio", OffsetMS: 9000}}
			var verified mediainfo.Info
			ctx := context.WithValue(context.Background(), struct{}{}, "caller context")
			start, end, err := Export(ctx, exportFixtureConfig(), inputs, edit.Range{StartMS: 12500, EndMS: 14500}, request, "output.mp4", func(ms int64) { reported = append(reported, ms) }, false, &verified, f)
			if err != nil || start != tc.start || end != 14500 || f.totalMS != tc.length || !reflect.DeepEqual(reported, []int64{123}) {
				t.Fatalf("copy source/output clocks changed: %d..%d progress=%v total=%d err=%v", start, end, reported, f.totalMS, err)
			}
			if f.keyframeInput.Path != inputs[0].Path || !f.keyframeInput.Remote || f.keyframeStart != 2500 || f.keyframeStream != 7 {
				t.Fatal("keyframe search lost its selected stream or source offset", f.keyframeInput, f.keyframeStart, f.keyframeStream)
			}
			if !reflect.DeepEqual(exportArgValues(f.args, "-ss"), tc.seeks) || !exportArgsContain(f.args, "-t", tc.trim) || !exportArgsContain(f.args, "-output_ts_offset", tc.shift) {
				t.Fatalf("copy seek/trim clocks changed: %v", f.args)
			}
			if !reflect.DeepEqual(exportArgValues(f.args, "-protocol_whitelist"), []string{"http,tcp", "file"}) || !reflect.DeepEqual(exportArgValues(f.args, "-format_whitelist"), []string{InputFormats, InputFormats}) || !exportArgsContain(f.args, "-map", "0:7", "-map", "1:11") {
				t.Fatal("input restrictions or absolute stream maps changed", f.args)
			}
			if exportArgsContain(f.args, "-movflags", "+faststart") || !reflect.DeepEqual(f.events, []string{"select", "keyframe", "run", "stat", "probe"}) || f.probeRemote || !reflect.DeepEqual(verified, f.output) {
				t.Fatal("intermediate finalization or fresh verification changed", f.events)
			}
			if f.selectedContext != ctx || f.runContext != ctx || f.probeContext != ctx {
				t.Fatal("coordinator replaced the caller's session/budget context")
			}
		})
	}
}

func TestSingleExportPreservesFailurePrecedenceAndErrorIdentity(t *testing.T) {
	raw, statFailure, probeFailure := errors.New("process cause"), errors.New("stat cause"), errors.New("probe cause")
	copyFailure, renderFailure := errors.New("copy diagnostic"), errors.New("render diagnostic")
	const noVideo = "result has no video stream"
	const geometry = "result video does not match the display geometry"
	const noAudio = "result has no selected audio stream"
	const duration = "result duration does not match the requested range"
	for _, tc := range []struct {
		name   string
		change func(*exportRuntimeFixture, *edit.ExportRequest)
		want   error
		text   string
		events []string
	}{
		{"full output beats cancellation", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.runErr, f.size = context.Canceled, 64<<20 }, ErrOutputLimit, "", []string{"select", "run", "stat"}},
		{"failed stat cannot replace process error", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.runErr, f.statErr = raw, statFailure }, raw, "", []string{"select", "run", "stat", "rendered"}},
		{"deadline remains the runtime cause", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.runErr = context.DeadlineExceeded }, context.DeadlineExceeded, "", []string{"select", "run", "stat", "rendered"}},
		{"copy before rendered classification", func(f *exportRuntimeFixture, r *edit.ExportRequest) {
			r.CutMode = "copy"
			f.keyframe = Keyframe{PTS: 2000, DTS: 1920, HasDTS: true}
			f.runErr, f.copyErr, f.renderedErr = raw, copyFailure, renderFailure
		}, renderFailure, "", []string{"select", "keyframe", "run", "stat", "copy", "rendered"}},
		{"success stat failure", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.statErr = statFailure }, statFailure, "", []string{"select", "run", "stat"}},
		{"successful producer at byte ceiling", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.size = 25_647_616 }, ErrOutputLimit, "", []string{"select", "run", "stat"}},
		{"fresh probe failure", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.probeErr = probeFailure }, probeFailure, "", []string{"select", "run", "stat", "probe"}},
		{"video before audio or duration", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.output.Streams, f.duration = nil, 9999 }, nil, noVideo, []string{"select", "run", "stat", "probe"}},
		{"geometry before audio or duration", func(f *exportRuntimeFixture, _ *edit.ExportRequest) {
			f.output.Streams = f.output.Streams[:1]
			f.output.Streams[0].Width = 80
			f.duration = 9999
		}, nil, geometry, []string{"select", "run", "stat", "probe"}},
		{"audio before duration", func(f *exportRuntimeFixture, _ *edit.ExportRequest) {
			f.output.Streams, f.duration = f.output.Streams[:1], 9999
		}, nil, noAudio, []string{"select", "run", "stat", "probe"}},
		{"duration after track verification", func(f *exportRuntimeFixture, _ *edit.ExportRequest) { f.duration = 2351 }, nil, duration, []string{"select", "run", "stat", "probe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, request := newExportRuntimeFixture(), exportFixtureRequest()
			tc.change(f, &request)
			verified := mediainfo.Info{}
			verified.Format.Duration = "unchanged"
			start, end, err := Export(context.Background(), exportFixtureConfig(), []Input{{Path: "input"}}, edit.Range{StartMS: 2000, EndMS: 4000}, request, "output.mp4", nil, true, &verified, f)
			if start != 0 || end != 0 || err == nil || (tc.want != nil && err != tc.want) || (tc.text != "" && err.Error() != tc.text) || verified.Format.Duration != "unchanged" || !reflect.DeepEqual(f.events, tc.events) {
				t.Fatalf("failure order/result changed: %d..%d err=%v verified=%+v events=%v", start, end, err, verified, f.events)
			}
			if tc.name == "copy before rendered classification" && (f.copyCause != raw || f.copyFormat != "mp4" || f.renderedCause != copyFailure) {
				t.Fatal("diagnostic adapters lost the original error chain")
			}
		})
	}
}

func TestSingleExportPreservesValidationAndCancellationOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, request := newExportRuntimeFixture(), exportFixtureRequest()
	request.Crop = &edit.Crop{Width: -1, Height: 1}
	_, _, err := Export(ctx, exportFixtureConfig(), nil, edit.Range{}, request, "output", nil, true, nil, f)
	var problem *mediainfo.Problem
	if !errors.As(err, &problem) || problem.Code != "crop_invalid" || len(f.events) != 0 {
		t.Fatalf("cancellation or I/O preceded crop validation: %v events=%v", err, f.events)
	}
	f, request = newExportRuntimeFixture(), exportFixtureRequest()
	f.selectErr = ctx.Err()
	_, _, err = Export(ctx, exportFixtureConfig(), nil, edit.Range{}, request, "output", nil, true, nil, f)
	if err != context.Canceled || !reflect.DeepEqual(f.events, []string{"select"}) {
		t.Fatalf("coordinator introduced an early context override: %v events=%v", err, f.events)
	}
	f = newExportRuntimeFixture()
	f.selection.Audio, f.selection.Video.Width = nil, 0
	_, _, err = Export(context.Background(), exportFixtureConfig(), []Input{{Path: "video"}, {Path: "audio"}}, edit.Range{EndMS: 2000}, request, "output", nil, true, nil, f)
	if err != ErrNoAudio || !reflect.DeepEqual(f.events, []string{"select"}) {
		t.Fatalf("missing paired audio no longer precedes geometry: %v events=%v", err, f.events)
	}
	f = newExportRuntimeFixture()
	_, _, err = Export(context.Background(), exportFixtureConfig(), []Input{{Path: "later source", OffsetMS: 3000}}, edit.Range{StartMS: 2000, EndMS: 4000}, request, "output", nil, true, nil, f)
	if err != mediainfo.ErrUnsupported || !reflect.DeepEqual(f.events, []string{"select"}) {
		t.Fatalf("negative local seek reached tool execution: %v events=%v", err, f.events)
	}
	cancelCtx, cancelFromReport := context.WithCancel(context.Background())
	defer cancelFromReport()
	f = newExportRuntimeFixture()
	f.onRun = func(runCtx context.Context, report func(int64)) error { report(123); return runCtx.Err() }
	verified := mediainfo.Info{}
	verified.Format.Duration = "not published"
	start, end, err := Export(cancelCtx, exportFixtureConfig(), []Input{{Path: "input"}}, edit.Range{EndMS: 2000}, request, "output", func(int64) { cancelFromReport() }, true, &verified, f)
	if err != context.Canceled || start != 0 || end != 0 || verified.Format.Duration != "not published" || !reflect.DeepEqual(f.events, []string{"select", "run", "stat", "rendered"}) {
		t.Fatalf("progress cancellation reached verification: %d..%d %v events=%v", start, end, err, f.events)
	}
}

func TestSingleExportPreservesRenderProfilesAndIntermediateOutputs(t *testing.T) {
	for _, tc := range []struct {
		name, profile, rendition, preset, crf string
		threads                               int
		final                                 bool
	}{
		{"default final", "", "", "ultrafast", "18", 0, true},
		{"compact intermediate", "compact", "", "veryfast", "20", 3, false},
		{"draft overrides compact", "compact", "draft", "ultrafast", "28", 33, false},
		{"unknown profile keeps historical fallback", "caller-profile", "", "ultrafast", "18", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, request, config := newExportRuntimeFixture(), exportFixtureRequest(), exportFixtureConfig()
			request.Quality, config.FFmpegProfile, config.FFmpegThreads = "720p", tc.profile, tc.threads
			f.selection.Audio.Channels, f.duration = 1, 2350
			ctx := CollectionRenderContext(context.Background(), tc.rendition)
			start, end, err := Export(ctx, config, []Input{{Path: "input"}}, edit.Range{StartMS: 2000, EndMS: 4000}, request, "output.mp4", nil, tc.final, nil, f)
			threads := "2"
			if tc.threads == 3 {
				threads = "3"
			}
			if err != nil || start != 2000 || end != 4350 || f.runHadReport || !exportArgsContain(f.args, "-preset", tc.preset, "-crf", tc.crf) || !exportArgsContain(f.args, "-threads", threads) || !exportArgsContain(f.args, "-b:a", "128k") {
				t.Fatalf("profile/thread/mono or inclusive duration tolerance changed: %d..%d err=%v args=%v", start, end, err, f.args)
			}
			// For two seconds at the 720p envelope: 5,781,600 bit/s plus 16 MiB.
			if !exportArgsContain(f.args, "-fs", "18222616") || exportArgsContain(f.args, "-movflags", "+faststart") != tc.final || !exportArgsContain(f.args, "-fps_mode:v", "passthrough", "-enc_time_base:v", "1/12800") {
				t.Fatal("requested-range byte ceiling, source clock or final-only faststart changed", f.args)
			}
		})
	}
	f, request := newExportRuntimeFixture(), exportFixtureRequest()
	request.Format = "mp3"
	f.output.Streams = f.output.Streams[1:]
	_, _, err := Export(context.Background(), exportFixtureConfig(), []Input{{Path: "input"}}, edit.Range{EndMS: 2000}, request, "output.mp3", nil, true, nil, f)
	if err != nil || !reflect.DeepEqual(exportArgValues(f.args, "-map"), []string{"0:11"}) || !exportArgsContain(f.args, "-vn", "-c:a", "libmp3lame", "-b:a", "192k") || !exportArgsContain(f.args, "-metadata:s:a:0", "language=rus") || !exportArgsContain(f.args, "-fs", strconv.FormatInt(16833216, 10)) {
		t.Fatalf("audio-only mapping, metadata or ceiling changed: %v args=%v", err, f.args)
	}
	f = newExportRuntimeFixture()
	f.selection.Video.ColorPrimaries, f.selection.Video.ColorTransfer, f.selection.Video.ColorSpace = "bt2020", "smpte2084", "bt2020nc"
	f.runErr = errors.New("HDR tool capability")
	_, _, err = Export(context.Background(), exportFixtureConfig(), []Input{{Path: "input"}}, edit.Range{EndMS: 2000}, exportFixtureRequest(), "output.mp4", nil, true, nil, f)
	if err != f.runErr || !f.hdr || f.renderedCause != f.runErr {
		t.Fatal("HDR failure classification lost its original cause or render state", err)
	}
}
