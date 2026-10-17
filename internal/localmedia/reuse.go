package localmedia

import (
	"context"
	"os"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediasession"
)

// ReuseLocalExport copies a verified result within the pinned local operation.
func ReuseLocalExport(ctx context.Context, source, prior, output string, expected os.FileInfo, limit int64) (bool, error) {
	reused, err := mediasession.Reuse(ctx, func() *mediasession.Session {
		if session := localExportSession(ctx, source, false); session != nil {
			return session.Session
		}
		return nil
	}, prior, output, expected, limit)
	return reused, mediaInfoError(err)
}
