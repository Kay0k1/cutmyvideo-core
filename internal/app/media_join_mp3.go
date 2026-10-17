package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"io"
	"os"
)

const ownedMP3ProbeMaxBytes = localmedia.OwnedMP3ProbeMaxBytes
const ownedMP3ProbeBufferBytes = localmedia.OwnedMP3ProbeBufferBytes
const ownedMP3DurationUnknown = localmedia.OwnedMP3DurationUnknown
const ownedMP3DurationRaw = localmedia.OwnedMP3DurationRaw
const ownedMP3DurationGapless = localmedia.OwnedMP3DurationGapless

func (p probeInfo) ownedMP3DurationMode() int {
	return localmedia.OwnedMP3DurationMode(mediainfo.Info(p))
}
func verifyJoinedOwnedMP3(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64, mode int) (probeInfo, error) {
	p, err := localmedia.VerifyJoinedOwnedMP3(ctx, c, path, request, expected, limit, mode)
	return probeInfo(p), localMediaError(err)
}
func probeOwnedMP3(ctx context.Context, path string, before os.FileInfo, mode int) (probeInfo, int64, bool, error) {
	p, d, ok, err := localmedia.ProbeOwnedMP3(ctx, path, before, mode)
	return probeInfo(p), d, ok, localMediaError(err)
}
func readOwnedMP3(ctx context.Context, input io.Reader, size int64) (localmedia.OwnedMP3Summary, bool, error) {
	return localmedia.ReadOwnedMP3(ctx, input, size)
}
func ownedMP3CRC(value uint16, data []byte) uint16 { return localmedia.OwnedMP3CRC(value, data) }
