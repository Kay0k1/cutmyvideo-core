package engine

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

type SubtitleRetimeRequest = subtitles.RetimeRequest
type SubtitleRetimeClip = subtitles.RetimeClip
type SubtitleSidecar = subtitles.Sidecar
type SubtitleRetimeResult = subtitles.RetimeResult
type SubtitleRetimeError = subtitles.RetimeError

// RetimeSubtitles converts externally supplied SRT/VTT into per-clip sidecars
// using actual segment maps. It requires no Engine, tools, database or files.
func RetimeSubtitles(ctx context.Context, request SubtitleRetimeRequest) (SubtitleRetimeResult, error) {
	return subtitles.Retime(ctx, request)
}
