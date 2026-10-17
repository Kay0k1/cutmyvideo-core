package localmedia

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/toolrun"
)

type processResponseBuffer struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (b *processResponseBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(p) <= remaining {
		return b.buffer.Write(p)
	}
	_, _ = b.buffer.Write(p[:remaining])
	b.overflow = true
	b.cancel()
	return remaining, mediaerror.ErrProcessResponseTooLarge
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &processResponseBuffer{limit: 8 << 20, cancel: cancel}
	err := runCommandOutput(runCtx, path, args, stdout)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.overflow {
		return nil, mediaerror.ErrProcessResponseTooLarge
	}
	if err != nil {
		return nil, err
	}
	return stdout.buffer.Bytes(), nil
}

func runCommandOutput(ctx context.Context, path string, args []string, stdout io.Writer) error {
	result := toolrun.RunOutput(ctx, path, args, stdout)
	observeProcess(ctx, path, result.Elapsed, result.ProcessErr)
	if err := result.ProcessErr; err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &mediaerror.ProcessFailure{Cause: err, Category: result.Category}
	}
	return nil
}

type processObserverKey struct{}
type ProcessObserver func(context.Context, string, time.Duration, error)

func WithProcessObserver(ctx context.Context, observer ProcessObserver) context.Context {
	return context.WithValue(ctx, processObserverKey{}, observer)
}
func observeProcess(ctx context.Context, path string, elapsed time.Duration, err error) {
	if observer, _ := ctx.Value(processObserverKey{}).(ProcessObserver); observer != nil {
		observer(ctx, path, elapsed, err)
	}
}
func RunCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	return runCommand(ctx, path, args...)
}
func RunCommandOutput(ctx context.Context, path string, args []string, stdout io.Writer) error {
	return runCommandOutput(ctx, path, args, stdout)
}
func FailureCategory(stderr string) string { return toolrun.FailureCategory(stderr) }
