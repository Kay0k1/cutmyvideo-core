package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// Keep service diagnostics at the boundary while the concrete effects belong
// to the same backend used by the local engine.
type joinedMediaRuntime struct{ mediaexport.JoinedRuntime }

func newJoinedMediaRuntime(c Config) joinedMediaRuntime {
	return joinedMediaRuntime{JoinedRuntime: localmedia.NewJoinedRuntime(c)}
}

var _ mediaexport.JoinedRuntime = joinedMediaRuntime{}

func (runtime joinedMediaRuntime) SelectInputs(ctx context.Context, inputs []mediaexport.Input, request edit.ExportRequest) (mediaexport.Selection, error) {
	selection, err := runtime.JoinedRuntime.SelectInputs(ctx, inputs, request)
	return selection, localMediaError(err)
}
func (runtime joinedMediaRuntime) NearestKeyframe(ctx context.Context, path string, start int64, remote bool, index int) (mediaexport.Keyframe, error) {
	frame, err := runtime.JoinedRuntime.NearestKeyframe(ctx, path, start, remote, index)
	return frame, localMediaError(err)
}
func (runtime joinedMediaRuntime) Run(ctx context.Context, args []string, total int64, report func(int64)) error {
	return localMediaError(runtime.JoinedRuntime.Run(ctx, args, total, report))
}
func (runtime joinedMediaRuntime) Probe(ctx context.Context, path string, remote bool) (mediainfo.Info, int64, error) {
	p, d, err := runtime.JoinedRuntime.Probe(ctx, path, remote)
	return p, d, localMediaError(err)
}
func (runtime joinedMediaRuntime) CopyDiagnostic(err error, format string) error {
	return localMediaError(runtime.JoinedRuntime.CopyDiagnostic(err, format))
}
func (runtime joinedMediaRuntime) RenderedFailure(err error, hdr bool) error {
	return localMediaError(runtime.JoinedRuntime.RenderedFailure(err, hdr))
}

type joinedMediaSource struct{ source mediaexport.SourceFence }

func (source *joinedMediaSource) Check() error { return localMediaError(source.source.Check()) }
func (runtime joinedMediaRuntime) PinnedSource(ctx context.Context, input mediaexport.Input) mediaexport.SourceFence {
	source := runtime.JoinedRuntime.PinnedSource(ctx, input)
	if source == nil {
		return nil
	}
	return &joinedMediaSource{source: source}
}
func (runtime joinedMediaRuntime) ProbeStreams(ctx context.Context, path string, remote bool) (mediainfo.Info, error) {
	p, err := runtime.JoinedRuntime.ProbeStreams(ctx, path, remote)
	return p, localMediaError(err)
}
func (runtime joinedMediaRuntime) MuxSubtitles(ctx context.Context, source string, info mediainfo.Info, media, out string, segments []edit.Segment, request edit.ExportRequest, limit int64) error {
	return localMediaError(runtime.JoinedRuntime.MuxSubtitles(ctx, source, info, media, out, segments, request, limit))
}
func (runtime joinedMediaRuntime) VerifyAccurate(ctx context.Context, out string, request edit.ExportRequest, expected, limit int64, selection mediaexport.Selection) (mediainfo.Info, error) {
	p, err := runtime.JoinedRuntime.VerifyAccurate(ctx, out, request, expected, limit, selection)
	return p, localMediaError(err)
}
func (runtime joinedMediaRuntime) SupportsAudioPacketDrop(ctx context.Context) (bool, error) {
	ok, err := runtime.JoinedRuntime.SupportsAudioPacketDrop(ctx)
	return ok, localMediaError(err)
}
