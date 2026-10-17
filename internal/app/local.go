package app

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

// InspectLocal and ExportLocal retain the shared app entrypoints while the
// concrete local backend is independent of the HTTP server and persistence.
func InspectLocal(ctx context.Context, c Config, path string) (Source, error) {
	source, e := localmedia.InspectLocal(ctx, c, path)
	return source, localMediaError(e)
}
func ExportLocal(ctx context.Context, c Config, path, out string, r Range, options ExportRequest) (int64, int64, error) {
	start, end, e := localmedia.ExportLocal(ctx, c, path, out, r, options)
	return start, end, localMediaError(e)
}
