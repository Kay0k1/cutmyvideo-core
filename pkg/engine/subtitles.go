package engine

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/app"
)

type SubtitleRetimeRequest = app.SubtitleRetimeRequest
type SubtitleRetimeClip = app.SubtitleRetimeClip
type SubtitleSidecar = app.SubtitleSidecar
type SubtitleRetimeResult = app.SubtitleRetimeResult
type SubtitleRetimeError = app.SubtitleRetimeError

// RetimeSubtitles converts externally supplied SRT/VTT into per-clip sidecars
// using actual segment maps. It requires no Engine, tools, database or files.
func RetimeSubtitles(ctx context.Context, request SubtitleRetimeRequest) (SubtitleRetimeResult, error) {
	return app.RetimeSubtitles(ctx, request)
}
