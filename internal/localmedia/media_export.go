package localmedia

import (
	"context"
	"os"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func mediaExportConfig(c Config) mediaexport.Config {
	return mediaexport.Config{FFmpegThreads: c.FFmpegThreads, FFmpegProfile: c.FFmpegProfile, MaxOutputBytes: c.MaxOutputBytes}
}

func mediaExportSelection(selection mediaSelection) mediaexport.Selection {
	return mediaexport.Selection{Video: selection.Video, Audio: selection.Audio, VideoInput: selection.VideoInput, AudioInput: selection.AudioInput, MP3DurationMode: selection.mp3DurationMode}
}

// Keep existing service diagnostics and callback causes at the boundary.
// Only direct coordinator errors are translated; wrapped errors pass through.
func mediaExportError(err error) error {
	if err == mediaexport.ErrNoAudio {
		return mediaerror.ErrNoAudio
	}
	if err == mediaexport.ErrOutputLimit {
		return mediaerror.ErrOutputLimit
	}
	return mediaInfoError(err)
}

type mediaExportRuntime struct{ config Config }

// NewRuntime exposes the concrete effect adapter to compatibility wrappers.
func NewRuntime(c Config) mediaexport.Runtime { return mediaExportRuntime{config: c} }

func (runtime mediaExportRuntime) SelectInputs(ctx context.Context, inputs []mediaexport.Input, request edit.ExportRequest) (mediaexport.Selection, error) {
	selection, err := probeInputSelectionOptions(ctx, runtime.config, inputs, ExportRequest(request))
	return mediaExportSelection(selection), err
}

func (runtime mediaExportRuntime) NearestKeyframe(ctx context.Context, path string, start int64, remote bool, streamIndex int) (mediaexport.Keyframe, error) {
	return nearestKeyframe(ctx, runtime.config, path, start, remote, streamIndex)
}

func (runtime mediaExportRuntime) Run(ctx context.Context, args []string, totalMS int64, report func(int64)) error {
	if report == nil {
		_, err := runCommand(ctx, runtime.config.FFmpeg, args...)
		return err
	}
	return runCommandOutput(ctx, runtime.config.FFmpeg, args, &mediaProgressWriter{totalMS: totalMS, report: report})
}

func (runtime mediaExportRuntime) StatSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (runtime mediaExportRuntime) Probe(ctx context.Context, path string, remote bool) (mediainfo.Info, int64, error) {
	info, duration, err := probe(ctx, runtime.config, path, remote)
	return mediainfo.Info(info), duration, err
}

func (runtime mediaExportRuntime) CopyDiagnostic(err error, format string) error {
	return mediaerror.CopyDiagnostic(err, format)
}

func (runtime mediaExportRuntime) RenderedFailure(err error, hdr bool) error {
	return mediaerror.RenderedFailure(err, hdr)
}
