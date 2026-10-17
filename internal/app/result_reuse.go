package app

import (
	"context"
	"os"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

// ReuseLocalExport copies a verified result within this operation. Every output
// owns its bytes; later edits to one exported file cannot change a duplicate.
func ReuseLocalExport(ctx context.Context, source, prior, output string, expected os.FileInfo, limit int64) (bool, error) {
	reused, err := localmedia.ReuseLocalExport(ctx, source, prior, output, expected, limit)
	return reused, localMediaError(err)
}
