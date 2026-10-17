package localmedia

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"io"
	"os"
)

// These adapters keep the service's private inspection tests attached to the
// same scanners used by the local engine.
const OwnedMP3ProbeMaxBytes = ownedMP3ProbeMaxBytes
const OwnedMP3ProbeBufferBytes = ownedMP3ProbeBufferBytes
const OwnedMP3DurationUnknown = ownedMP3DurationUnknown
const OwnedMP3DurationRaw = ownedMP3DurationRaw
const OwnedMP3DurationGapless = ownedMP3DurationGapless
const OwnedMP4MaxFileBytes = ownedMP4MaxFileBytes
const OwnedMP4MaxReadBytes = ownedMP4MaxReadBytes

type OwnedMP3Summary = ownedMP3Summary

func OwnedMP3DurationMode(p mediainfo.Info) int { return probeInfo(p).ownedMP3DurationMode() }
func ProbeOwnedMP3(ctx context.Context, path string, before os.FileInfo, mode int) (mediainfo.Info, int64, bool, error) {
	p, d, ok, err := probeOwnedMP3(ctx, path, before, mode)
	return mediainfo.Info(p), d, ok, err
}
func VerifyJoinedOwnedMP3(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64, mode int) (mediainfo.Info, error) {
	p, err := verifyJoinedOwnedMP3(ctx, c, path, request, expected, limit, mode)
	return mediainfo.Info(p), err
}
func ReadOwnedMP3(ctx context.Context, input io.Reader, size int64) (OwnedMP3Summary, bool, error) {
	return readOwnedMP3(ctx, input, size)
}
func OwnedMP3CRC(value uint16, data []byte) uint16 { return ownedMP3CRC(value, data) }
func ProbeOwnedMP4(ctx context.Context, path string, before os.FileInfo) (mediainfo.Info, int64, bool, error) {
	p, d, ok, err := probeOwnedMP4(ctx, path, before)
	return mediainfo.Info(p), d, ok, err
}
func VerifyJoinedOwnedMP4(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64, selection mediaexport.Selection) (mediainfo.Info, error) {
	original := mediaSelection{Video: selection.Video, Audio: selection.Audio, VideoInput: selection.VideoInput, AudioInput: selection.AudioInput, mp3DurationMode: selection.MP3DurationMode}
	p, err := verifyJoinedOwnedMP4(ctx, c, path, request, expected, limit, original)
	return mediainfo.Info(p), err
}
func InspectOwnedMP4(ctx context.Context, file io.ReaderAt, size, read int64) (mediainfo.Info, int64, bool, int64) {
	reader := ownedMP4Reader{ctx: ctx, file: file, size: size, read: read}
	p, d, ok := reader.inspect()
	return mediainfo.Info(p), d, ok, reader.read
}
