package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

type subtitleCue = subtitles.Cue

func parseSRT(data []byte) ([]subtitleCue, error) { return subtitles.ParseSourceSRT(data) }
func subtitleTimestamp(ms int64) string           { return subtitles.Timestamp(ms) }
func rebaseSubtitles(cues []subtitleCue, segments []ArtifactSegment) ([]byte, error) {
	data, err := localmedia.RebaseSubtitles(cues, segments)
	return data, localMediaError(err)
}
func exportInputsProgress(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64)) (int64, int64, error) {
	start, end, err := localmedia.ExportInputsProgress(ctx, c, inputs, r, request, out, report)
	return start, end, localMediaError(err)
}
