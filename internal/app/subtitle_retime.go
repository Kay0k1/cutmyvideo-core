package app

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

type SubtitleRetimeRequest = subtitles.RetimeRequest
type SubtitleRetimeClip = subtitles.RetimeClip
type SubtitleSidecar = subtitles.Sidecar
type SubtitleRetimeResult = subtitles.RetimeResult
type SubtitleRetimeError = subtitles.RetimeError

// RetimeSubtitles produces bounded UTF-8 sidecars using actual export maps.
// Intervals are half-open. Source cuts may repeat or be reordered; output maps
// must be ordered and non-overlapping. Clocks are translated, never stretched.
func RetimeSubtitles(ctx context.Context, request SubtitleRetimeRequest) (SubtitleRetimeResult, error) {
	return subtitles.Retime(ctx, request)
}
