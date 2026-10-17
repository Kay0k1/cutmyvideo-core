package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"io"
	"os"
)

const ownedMP4MaxFileBytes = localmedia.OwnedMP4MaxFileBytes
const ownedMP4MaxReadBytes = localmedia.OwnedMP4MaxReadBytes

func verifyJoinedOwnedMP4(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64, selection mediaSelection) (probeInfo, error) {
	p, err := localmedia.VerifyJoinedOwnedMP4(ctx, c, path, request, expected, limit, mediaExportSelection(selection))
	return probeInfo(p), localMediaError(err)
}
func probeOwnedMP4(ctx context.Context, path string, before os.FileInfo) (probeInfo, int64, bool, error) {
	p, d, ok, err := localmedia.ProbeOwnedMP4(ctx, path, before)
	return probeInfo(p), d, ok, localMediaError(err)
}

type ownedMP4Reader struct {
	ctx        context.Context
	file       io.ReaderAt
	size, read int64
}

func (r *ownedMP4Reader) inspect() (probeInfo, int64, bool) {
	p, d, ok, read := localmedia.InspectOwnedMP4(r.ctx, r.file, r.size, r.read)
	r.read = read
	return probeInfo(p), d, ok
}
