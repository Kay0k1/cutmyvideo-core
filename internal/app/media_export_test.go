package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func TestMediaExportBridgePreservesLegacyErrorsAndRuntime(t *testing.T) {
	for _, test := range []struct{ neutral, legacy error }{
		{nil, nil},
		{mediaexport.ErrNoAudio, errNoAudio},
		{mediaexport.ErrOutputLimit, errOutputLimit},
		{mediainfo.ErrUnsupported, errUnsupportedStream},
	} {
		if got := mediaExportError(test.neutral); got != test.legacy {
			t.Fatal("coordinator sentinel lost its legacy identity", got)
		}
	}
	for _, code := range []string{"crop_invalid", "copy_quality_unsupported"} {
		err := mediaExportError(&mediainfo.Problem{Code: code, Message: "fixed diagnostic"})
		problem, ok := err.(*sourceProblem)
		if !ok || problem.code != code || problem.message != "fixed diagnostic" || errors.Unwrap(err) != nil {
			t.Fatal("coordinator diagnostic changed its concrete legacy shape", err)
		}
	}
	for _, original := range []error{
		errNoAudio, errOutputLimit, errUnsupportedStream,
		&sourceProblem{"audio_track_unavailable", "fixed legacy diagnostic"},
		&mediaProcessFailure{cause: context.Canceled, category: "unknown"},
		&os.PathError{Op: "open", Path: "fixture", Err: os.ErrPermission},
		fmt.Errorf("wrapped neutral sentinel: %w", mediaexport.ErrNoAudio),
		fmt.Errorf("wrapped cancellation: %w", context.Canceled),
	} {
		if got := mediaExportError(original); got != original {
			t.Fatal("callback error was replaced or wrapped", original)
		}
	}
	config := Config{FFmpegThreads: 37, FFmpegProfile: "unmodified", MaxOutputBytes: 7, FFprobe: "unused-probe"}
	if got := mediaExportConfig(config); got != (mediaexport.Config{FFmpegThreads: 37, FFmpegProfile: "unmodified", MaxOutputBytes: 7}) {
		t.Fatal("coordinator projection changed caller configuration", got)
	}
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("pinned fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, session := BeginLocalExport(context.Background(), path)
	defer session.Close()
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	session.File = file
	session.Info = mediainfo.Info{Streams: []probeStream{
		{Index: 2, CodecType: "video", Width: 100, Height: 100},
		{Index: 4, CodecType: "audio", Channels: 1},
		{Index: 7, CodecType: "audio", Channels: 2},
	}}
	session.Info.Format.Duration = "3.000"
	runtime := mediaExportRuntime{config: config}
	selectedAudio := 7
	selection, err := runtime.SelectInputs(ctx, []mediaexport.Input{{Path: path}}, edit.ExportRequest{AudioTrack: &selectedAudio})
	if err != nil || selection.Video != &session.Info.Streams[0] || selection.Audio != &session.Info.Streams[2] || selection.VideoInput != 0 || selection.AudioInput != 0 {
		t.Fatal("runtime changed local session selection or stream identity", selection, err)
	}
	if got := selection.MapArgs("mp4"); !reflect.DeepEqual(got, []string{"-map", "0:2", "-map", "0:7"}) {
		t.Fatal("projected stream mapping changed", got)
	}
	info, duration, err := runtime.Probe(ctx, path, false)
	if err != nil || duration != 3000 || &info.Streams[0] != &session.Info.Streams[0] {
		t.Fatal("runtime bypassed the verified local probe session", duration, err)
	}
	from, until := keyframeProbeBounds(1000)
	frame := copyKeyframe{PTS: 500, DTS: 450, HasDTS: true}
	session.KeyframeFile = file
	session.Keyframes = []keyframeWindow{{From: from, Until: until, Stream: 2, Tool: config.FFprobe, Frames: []copyKeyframe{frame}}}
	if got, err := runtime.NearestKeyframe(ctx, path, 1000, false, 2); err != nil || got != frame {
		t.Fatal("runtime lost the local keyframe session", got, err)
	}
	if size, err := runtime.StatSize(path); err != nil || size != file.Size() {
		t.Fatal("runtime stat changed file size", size, err)
	}
	if _, err := runtime.StatSize(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime stat lost its OS cause", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := runtime.SelectInputs(cancelled, []mediaexport.Input{{Path: path}}, edit.ExportRequest{}); err != context.Canceled {
		t.Fatal("runtime selection lost cancellation identity", err)
	}
	cause := errors.New("native failure")
	copyFailure := &mediaProcessFailure{cause: cause, category: "copy_incompatible"}
	copyErr := runtime.CopyDiagnostic(copyFailure, "mp4")
	var legacyCopy *copyExportFailure
	if !errors.As(copyErr, &legacyCopy) || !errors.Is(copyErr, cause) || LocalErrorCode(copyErr) != "copy_incompatible" {
		t.Fatal("runtime changed copy diagnostic causes", copyErr)
	}
	hdrErr := runtime.RenderedFailure(&mediaProcessFailure{cause: cause, category: "hdr_conversion_unavailable"}, true)
	if !errors.Is(hdrErr, cause) || LocalErrorCode(hdrErr) != "hdr_conversion_unavailable" {
		t.Fatal("runtime changed HDR diagnostic causes", hdrErr)
	}
}

func TestMediaExportBackendPreservesProcessPaths(t *testing.T) {
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == "cutmy-media-export-bridge-output" {
		_, _ = io.WriteString(os.Stdout, "out_time_us=2000000\n")
		_, _ = io.CopyN(os.Stdout, strings.NewReader(strings.Repeat("x", 9<<20)), 9<<20)
		_, _ = io.WriteString(os.Stdout, "\nout_time_us=9000000\nout_time_us=1000\n")
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtime := mediaExportRuntime{config: Config{FFmpeg: executable}}
	args := []string{"-test.run=^TestMediaExportBackendPreservesProcessPaths$", "--", "cutmy-media-export-bridge-output"}
	t.Run("buffered", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := runtime.Run(ctx, args, 3000, nil); err != errProcessResponseTooLarge {
			t.Fatal("nil progress callback bypassed the legacy stdout memory bound", err)
		}
	})
	t.Run("streamed", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var progress []int64
		if err := runtime.Run(ctx, args, 3000, func(ms int64) { progress = append(progress, ms) }); err != nil {
			t.Fatal("progress execution acquired the buffered stdout limit", err)
		}
		if !reflect.DeepEqual(progress, []int64{2000, 3000}) {
			t.Fatal("runtime lost bounded monotonic progress", progress)
		}
	})
}
