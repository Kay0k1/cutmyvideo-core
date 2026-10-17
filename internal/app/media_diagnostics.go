package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type mediaDiagnosticsKey struct{}
type mediaDiagnostics struct {
	jobID, itemID, ffmpeg, ffprobe string
}

// Only worker-owned identifiers and fixed tool labels reach logs. Tool paths
// are retained solely for comparison and never serialized with user arguments.
func withMediaDiagnostics(ctx context.Context, c Config, jobID, itemID string) context.Context {
	ctx = context.WithValue(ctx, mediaDiagnosticsKey{}, mediaDiagnostics{jobID: jobID, itemID: itemID, ffmpeg: c.FFmpeg, ffprobe: c.FFprobe})
	return localmedia.WithProcessObserver(ctx, recordMediaProcess)
}

func recordMediaProcess(ctx context.Context, path string, elapsed time.Duration, err error) {
	metadata, ok := ctx.Value(mediaDiagnosticsKey{}).(mediaDiagnostics)
	if !ok || metadata.jobID == "" {
		return
	}
	tool := ""
	if path == metadata.ffmpeg && path != "" {
		tool = "ffmpeg"
	} else if path == metadata.ffprobe && path != "" {
		tool = "ffprobe"
	}
	if tool == "" {
		return
	}
	slog.Info("media_subprocess_finished", "job_id", metadata.jobID, "item_id", metadata.itemID, "tool", tool, "elapsed_ms", elapsed.Milliseconds(), "success", err == nil)
}
