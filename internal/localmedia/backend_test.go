package localmedia

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func TestLocalMediaSessionPreservesOwnershipAndFreshness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("original pinned source"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, session := BeginLocalExport(context.Background(), path)
	defer session.Close()
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	session.File = file
	session.Info.Streams = []mediainfo.Stream{
		{Index: 0, CodecType: "video", CodecName: "h264", Width: 160, Height: 90},
		{Index: 1, CodecType: "audio", CodecName: "aac", Channels: 1},
		{Index: 2, CodecType: "audio", CodecName: "aac", Channels: 2},
	}
	session.Info.Format.Duration = "3.000"
	config := Config{FFprobe: "unused exact probe identity", DatabaseURL: "not opened", FFmpegThreads: 37}
	info, duration, err := Probe(ctx, config, path, false)
	if err != nil || duration != 3000 || &info.Streams[0] != &session.Info.Streams[0] || GetLocalExportSession(ctx, path, false) != session || GetLocalExportSession(ctx, path, true) != nil || GetLocalExportSession(ctx, path+"-different", false) != nil {
		t.Fatal("inspection lost its operation-local identity or shared stream storage", duration, err)
	}
	audio := 2
	selection, err := ProbeInputSelectionOptions(ctx, config, []mediaInput{{Path: path}}, ExportRequest{AudioTrack: &audio})
	if err != nil || selection.Video != &session.Info.Streams[0] || selection.Audio != &session.Info.Streams[2] {
		t.Fatal("explicit audio selection bypassed the shared source inspection", selection, err)
	}
	selection, err = ProbeInputSelectionOptions(ctx, config, []mediaInput{{Path: path}}, ExportRequest{AudioTrack: &audio, NoAudio: true})
	if err != nil || selection.Audio != nil {
		t.Fatal("no-audio no longer precedes the explicit track selector", selection, err)
	}
	cause := errors.New("original keyframe callback failure")
	called := false
	_, err = session.NearestKeyframeWithProbe(ctx, config, 1000, 3,
		func(got context.Context, gotConfig Config, gotPath string, remote bool, stream int, from, until int64) ([]copyKeyframe, error) {
			called = true
			if got != ctx || gotConfig != config || gotPath != path || remote || stream != 3 || from != 0 || until != 1001 {
				t.Fatal("session callback arguments changed")
			}
			return nil, cause
		})
	if !called || err != cause {
		t.Fatal("session replaced an opaque callback failure", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := Probe(cancelled, config, path, false); err != context.Canceled {
		t.Fatal("cancelled cached inspection lost its cancellation identity", err)
	}
	if err := os.WriteFile(path, []byte("different source bytes and length"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = Probe(ctx, config, path, false)
	problem, ok := err.(*mediaerror.Problem)
	if !ok || problem.Code != "source_changed" || errors.Unwrap(err) != nil {
		t.Fatal("cached metadata survived source replacement", err)
	}
}

func TestLocalMediaProcessPreservesBoundsStreamingAndObserver(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestLocalMediaProcessFixture$", "--", "cutmy-localmedia-large-output"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	observations := 0
	ctx = WithProcessObserver(ctx, func(got context.Context, path string, elapsed time.Duration, failure error) {
		observations++
		if got.Err() != nil && !errors.Is(got.Err(), context.Canceled) || path != executable || elapsed < 0 {
			t.Fatal("process observer lost its configured tool or operation context")
		}
	})
	data, err := RunCommand(ctx, executable, args...)
	if err != mediaerror.ErrProcessResponseTooLarge || data != nil || observations != 1 {
		t.Fatal("buffered output bypassed its 8 MiB bound or returned an error prefix", len(data), err, observations)
	}
	var progress []int64
	if err := NewRuntime(Config{FFmpeg: executable}).Run(ctx, args, 3000, func(ms int64) { progress = append(progress, ms) }); err != nil || observations != 2 || len(progress) != 2 || progress[0] != 1000 || progress[1] != 3000 {
		t.Fatal("streamed progress inherited the buffered byte limit or lost monotonic bounds", progress, err)
	}
	failureCtx, cancelFailure := context.WithCancel(context.Background())
	defer cancelFailure()
	var rawFailure error
	failureCtx = WithProcessObserver(failureCtx, func(got context.Context, path string, _ time.Duration, failure error) {
		if got != failureCtx || path != executable {
			t.Fatal("process observer context was replaced")
		}
		rawFailure = failure
		cancelFailure()
	})
	err = RunCommandOutput(failureCtx, executable, []string{"-test.run=^TestLocalMediaProcessFixture$", "--", "cutmy-localmedia-fail"}, io.Discard)
	if rawFailure == nil || err != context.Canceled || errors.Is(rawFailure, context.Canceled) {
		t.Fatal("raw observation no longer precedes cancellation classification", rawFailure, err)
	}
}

func TestLocalMediaProcessFixture(t *testing.T) {
	if len(os.Args) == 0 {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "cutmy-localmedia-large-output":
		_, _ = io.WriteString(os.Stdout, "out_time_us=1000000\n")
		_, _ = io.CopyN(os.Stdout, strings.NewReader(strings.Repeat("x", 9<<20)), 9<<20)
		_, _ = io.WriteString(os.Stdout, "\nout_time_us=9000000\nout_time_us=500000\n")
		os.Exit(0)
	case "cutmy-localmedia-fail":
		os.Exit(9)
	}
}
