package app

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
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
		return errNoAudio
	}
	if err == mediaexport.ErrOutputLimit {
		return errOutputLimit
	}
	return mediaInfoError(err)
}

type mediaExportRuntime struct{ config Config }

func (runtime mediaExportRuntime) RenderRuntime() mediaexport.RenderRuntime {
	return localmedia.NewRenderRuntime(runtime.config)
}

func (runtime mediaExportRuntime) SelectInputs(ctx context.Context, inputs []mediaexport.Input, request edit.ExportRequest) (mediaexport.Selection, error) {
	selection, err := localmedia.NewRuntime(runtime.config).SelectInputs(ctx, inputs, request)
	return selection, localMediaError(err)
}

func (runtime mediaExportRuntime) NearestKeyframe(ctx context.Context, path string, start int64, remote bool, streamIndex int) (mediaexport.Keyframe, error) {
	frame, err := localmedia.NewRuntime(runtime.config).NearestKeyframe(ctx, path, start, remote, streamIndex)
	return frame, localMediaError(err)
}

func (runtime mediaExportRuntime) Run(ctx context.Context, args []string, totalMS int64, report func(int64)) error {
	return localMediaError(localmedia.NewRuntime(runtime.config).Run(ctx, args, totalMS, report))
}

func (runtime mediaExportRuntime) StatSize(path string) (int64, error) {
	return localmedia.NewRuntime(runtime.config).StatSize(path)
}

func (runtime mediaExportRuntime) Probe(ctx context.Context, path string, remote bool) (mediainfo.Info, int64, error) {
	info, duration, err := localmedia.NewRuntime(runtime.config).Probe(ctx, path, remote)
	return info, duration, localMediaError(err)
}

func (runtime mediaExportRuntime) CopyDiagnostic(err error, format string) error {
	return localMediaError(localmedia.NewRuntime(runtime.config).CopyDiagnostic(err, format))
}

func (runtime mediaExportRuntime) RenderedFailure(err error, hdr bool) error {
	return localMediaError(localmedia.NewRuntime(runtime.config).RenderedFailure(err, hdr))
}
