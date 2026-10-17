package localmedia

import (
	"context"
	"io"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/toolrun"
)

func (runtime mediaExportRuntime) RunWithDiagnostics(ctx context.Context, args []string) ([]byte, error) {
	if err := CheckAudioReplacement(ctx); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	report := &processResponseBuffer{limit: 64 << 10, cancel: cancel}
	r := toolrun.RunOutputWithDiagnostics(runCtx, runtime.config.FFmpeg, args, io.Discard, report)
	observeProcess(ctx, runtime.config.FFmpeg, r.Elapsed, r.ProcessErr)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if report.overflow {
		return nil, mediaerror.ErrProcessResponseTooLarge
	}
	if r.ProcessErr != nil {
		return nil, &mediaerror.ProcessFailure{Cause: r.ProcessErr, Category: r.Category}
	}
	if err := CheckAudioReplacement(ctx); err != nil {
		return nil, err
	}
	return report.buffer.Bytes(), nil
}
