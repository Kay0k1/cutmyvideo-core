package localmedia

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
)

func (runtime mediaExportRuntime) RenderRuntime() mediaexport.RenderRuntime {
	return newJoinedMediaRuntime(runtime.config)
}
func NewRenderRuntime(c Config) mediaexport.RenderRuntime { return newJoinedMediaRuntime(c) }

func (runtime joinedMediaRuntime) CheckSource(ctx context.Context, input mediaexport.Input) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CheckAudioReplacement(ctx); err != nil {
		return err
	}
	if pinned := runtime.PinnedSource(ctx, input); pinned != nil {
		return pinned.Check()
	}
	return nil
}
