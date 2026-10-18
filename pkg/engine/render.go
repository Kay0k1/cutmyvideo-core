package engine

import (
	"context"
	"errors"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func prepareEngineReplacement(ctx context.Context, request localmedia.ExportRequest) (context.Context, func(), error) {
	if request.AudioReplace != nil && request.AudioReplace.SourceID != "" {
		return ctx, func() {}, invalidArgument(errors.New("local engine audio_replace requires input, not source_id"))
	}
	return localmedia.PrepareAudioReplacement(ctx, request)
}
