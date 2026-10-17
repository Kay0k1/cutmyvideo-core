package localmedia

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"io"
	"math"
	"os"
	"strconv"
)

const ownedMP3ProbeMaxBytes = 1 << 20
const ownedMP3ProbeBufferBytes = 16 << 10
const ownedMP3ProbeMaxID3Bytes = 64 << 10

const (
	ownedMP3DurationUnknown = iota
	ownedMP3DurationRaw
	ownedMP3DurationGapless
)

// FFprobe 8 changed MP3 format duration to exclude encoder delay/padding.
// Obtain its actual library family in the existing source inspection, since
// callers can configure FFmpeg and FFprobe independently. Unknown families
// keep native result verification, without an extra version-query process.
func (p probeInfo) ownedMP3DurationMode() int {
	if len(p.LibraryVersions) > 16 {
		return ownedMP3DurationUnknown
	}
	major := 0
	seen := false
	for _, library := range p.LibraryVersions {
		if library.Name != "libavformat" {
			continue
		}
		if seen {
			return ownedMP3DurationUnknown
		}
		seen = true
		major = library.Major
	}
	switch major {
	case 59, 60, 61:
		return ownedMP3DurationRaw
	case 62, 63:
		return ownedMP3DurationGapless
	default:
		return ownedMP3DurationUnknown
	}
}

type ownedMP3Summary struct {
	frames, rate, channels   int64
	startPadding, endPadding int64
}

type ownedMP3Header struct {
	bytes, rate, channels int
}

// Called ONLY after the accurate join's own libmp3lame/192k encoder succeeded.
// General source inspection and copied outputs continue to use fresh FFprobe.
// This fresh bounded scan proves the supported metadata/frame envelope and
// FFmpeg's audio-payload CRC, not arbitrary MP3 decodability. Unknown shapes
// return to the established probe, and independent decode/clock gates remain.
func verifyJoinedOwnedMP3(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64, durationMode int) (probeInfo, error) {
	if request.Format != "mp3" || durationMode == ownedMP3DurationUnknown {
		return verifyJoined(ctx, c, path, request, expected, limit)
	}
	stat, err := os.Stat(path)
	if err != nil {
		return probeInfo{}, err
	}
	if stat.Size() >= limit {
		return probeInfo{}, mediaerror.ErrOutputLimit
	}
	p, duration, supported, err := probeOwnedMP3(ctx, path, stat, durationMode)
	if err != nil {
		return probeInfo{}, err
	}
	if !supported {
		return verifyJoined(ctx, c, path, request, expected, limit)
	}
	if err := p.validateStreams(); err != nil {
		return verifyJoined(ctx, c, path, request, expected, limit)
	}
	if math.Abs(float64(duration-expected)) > 350 {
		return p, fmt.Errorf("joined result duration does not match its selected intervals")
	}
	return p, nil
}

func probeOwnedMP3(ctx context.Context, path string, before os.FileInfo, durationMode int) (p probeInfo, duration int64, supported bool, err error) {
	if err := ctx.Err(); err != nil {
		return p, 0, false, err
	}
	if (durationMode != ownedMP3DurationRaw && durationMode != ownedMP3DurationGapless) || before == nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > ownedMP3ProbeMaxBytes {
		return p, 0, false, nil
	}
	pathInfo, openErr := os.Lstat(path)
	if openErr != nil || !sameOwnedMP3File(before, pathInfo) {
		return p, 0, false, nil
	}
	file, openErr := os.Open(path)
	if openErr != nil {
		return p, 0, false, nil
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			p, duration, supported = probeInfo{}, 0, false
		}
	}()
	opened, statErr := file.Stat()
	if statErr != nil || !sameOwnedMP3File(before, opened) {
		return p, 0, false, nil
	}
	summary, supported, err := readOwnedMP3(ctx, file, opened.Size())
	if err != nil || !supported {
		return p, 0, false, err
	}
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !sameOwnedMP3File(opened, after) || !sameOwnedMP3File(opened, current) {
		return p, 0, false, nil
	}
	if err := ctx.Err(); err != nil {
		return p, 0, false, err
	}
	// Match the actual configured FFprobe family's format duration.
	samples := summary.frames * 1152
	if durationMode == ownedMP3DurationGapless {
		samples -= summary.startPadding + summary.endPadding
	}
	if samples <= 0 {
		return p, 0, false, nil
	}
	micros := (samples*1_000_000 + summary.rate/2) / summary.rate
	p.Format.Duration = strconv.FormatInt(micros/1_000_000, 10) + fmt.Sprintf(".%06d", micros%1_000_000)
	p.Streams = []probeStream{{Index: 0, CodecType: "audio", CodecName: "mp3", SampleRate: strconv.FormatInt(summary.rate, 10), Channels: int(summary.channels), TimeBase: "1/14112000"}}
	seconds, _ := strconv.ParseFloat(p.Format.Duration, 64)
	duration = int64(math.Round(seconds * 1000))
	return p, duration, true, nil
}

func sameOwnedMP3File(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func readOwnedMP3(ctx context.Context, input io.Reader, size int64) (ownedMP3Summary, bool, error) {
	var result ownedMP3Summary
	if err := ctx.Err(); err != nil {
		return result, false, err
	}
	if size <= 0 || size > ownedMP3ProbeMaxBytes {
		return result, false, nil
	}
	// LimitReader also bounds prefetch if a file grows during the scan. The
	// opened-handle/path fence detects that change before this result is used.
	r := bufio.NewReaderSize(io.LimitReader(input, size), ownedMP3ProbeBufferBytes)
	tagBytes, supported, err := ownedMP3ID3(ctx, r, size)
	if err != nil || !supported {
		return result, false, err
	}
	remaining := size - tagBytes
	headerBytes, readErr := r.Peek(4)
	if readErr != nil {
		return result, false, ctx.Err()
	}
	first, supported := ownedMP3FrameHeader(headerBytes)
	if !supported || int64(first.bytes) > remaining {
		return result, false, nil
	}
	frame, readErr := r.Peek(first.bytes)
	if readErr != nil {
		return result, false, ctx.Err()
	}
	infoOffset := 4 + 32
	if first.channels == 1 {
		infoOffset = 4 + 17
	}
	if first.bytes < infoOffset+156 || string(frame[infoOffset:infoOffset+4]) != "Info" || binary.BigEndian.Uint32(frame[infoOffset+4:]) != 15 {
		return result, false, nil
	}
	for _, b := range frame[4:infoOffset] {
		if b != 0 {
			return result, false, nil
		}
	}
	for _, b := range frame[infoOffset+156:] {
		if b != 0 {
			return result, false, nil
		}
	}
	producer := string(frame[infoOffset+120 : infoOffset+124])
	if producer != "Lavf" && producer != "Lavc" && producer != "LAME" {
		return result, false, nil
	}
	frames := int64(binary.BigEndian.Uint32(frame[infoOffset+8:]))
	bytes := int64(binary.BigEndian.Uint32(frame[infoOffset+12:]))
	musicBytes := int64(binary.BigEndian.Uint32(frame[infoOffset+148:]))
	musicCRC := binary.BigEndian.Uint16(frame[infoOffset+152:])
	framePadding := [3]byte{frame[infoOffset+141], frame[infoOffset+142], frame[infoOffset+143]}
	if frames <= 0 || frames > size/4 || bytes != remaining || musicBytes != bytes {
		return result, false, nil
	}
	if _, readErr = r.Discard(first.bytes); readErr != nil {
		return result, false, ctx.Err()
	}
	remaining -= int64(first.bytes)
	var count int64
	var checksum uint16
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return result, false, err
		}
		headerBytes, readErr = r.Peek(4)
		if readErr != nil {
			return result, false, ctx.Err()
		}
		header, valid := ownedMP3FrameHeader(headerBytes)
		if !valid || header.rate != first.rate || header.channels != first.channels || int64(header.bytes) > remaining {
			return result, false, nil
		}
		frame, readErr = r.Peek(header.bytes)
		if readErr != nil {
			return result, false, ctx.Err()
		}
		checksum = ownedMP3CRC(checksum, frame)
		if _, readErr = r.Discard(header.bytes); readErr != nil {
			return result, false, ctx.Err()
		}
		remaining -= int64(header.bytes)
		count++
		if count > frames {
			return result, false, nil
		}
	}
	if count != frames || checksum != musicCRC {
		return result, false, nil
	}
	if _, readErr = r.Peek(1); readErr != io.EOF {
		return result, false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return result, false, err
	}
	padding := uint32(framePadding[0])<<16 | uint32(framePadding[1])<<8 | uint32(framePadding[2])
	return ownedMP3Summary{frames: frames, rate: int64(first.rate), channels: int64(first.channels), startPadding: int64(padding >> 12), endPadding: int64(padding & 4095)}, true, nil
}

func ownedMP3ID3(ctx context.Context, r *bufio.Reader, fileSize int64) (int64, bool, error) {
	header, err := r.Peek(10)
	if err != nil {
		return 0, false, ctx.Err()
	}
	if string(header[:3]) != "ID3" || (header[3] != 3 && header[3] != 4) || header[4] != 0 || header[5] != 0 {
		return 0, false, nil
	}
	version := header[3]
	size, valid := ownedMP3Syncsafe(header[6:10])
	if !valid || size > ownedMP3ProbeMaxID3Bytes || int64(size)+10 >= fileSize {
		return 0, false, nil
	}
	if _, err := r.Discard(10); err != nil {
		return 0, false, ctx.Err()
	}
	for remaining := size; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		chunk, err := r.Peek(min(remaining, 10))
		if err != nil {
			return 0, false, ctx.Err()
		}
		if chunk[0] == 0 {
			// ID3 padding is accepted only when every remaining byte is zero.
			for remaining > 0 {
				if err := ctx.Err(); err != nil {
					return 0, false, err
				}
				n := min(remaining, ownedMP3ProbeBufferBytes)
				padding, err := r.Peek(n)
				if err != nil {
					return 0, false, ctx.Err()
				}
				for _, b := range padding {
					if b != 0 {
						return 0, false, nil
					}
				}
				_, _ = r.Discard(n)
				remaining -= n
			}
			break
		}
		if remaining < 10 || string(chunk[:4]) != "TSSE" || chunk[8] != 0 || chunk[9] != 0 {
			// Do not skip APIC/unknown fields and then claim a single valid
			// audio stream. Full FFprobe retains all metadata/stream checks.
			return 0, false, nil
		}
		length := int(binary.BigEndian.Uint32(chunk[4:8]))
		if version == 4 {
			length, valid = ownedMP3Syncsafe(chunk[4:8])
			if !valid {
				return 0, false, nil
			}
		}
		if length < 5 || length > 128 || length > remaining-10 {
			return 0, false, nil
		}
		_, _ = r.Discard(10)
		text, err := r.Peek(length)
		if err != nil {
			return 0, false, ctx.Err()
		}
		if (text[0] != 0 && text[0] != 3) || string(text[1:5]) != "Lavf" {
			return 0, false, nil
		}
		for _, b := range text[1:] {
			if b >= 128 {
				return 0, false, nil
			}
		}
		_, _ = r.Discard(length)
		remaining -= 10 + length
	}
	return int64(size) + 10, true, nil
}

func ownedMP3Syncsafe(data []byte) (int, bool) {
	if len(data) != 4 {
		return 0, false
	}
	var value int
	for _, b := range data {
		if b&128 != 0 {
			return 0, false
		}
		value = value<<7 | int(b)
	}
	return value, true
}

func ownedMP3FrameHeader(data []byte) (ownedMP3Header, bool) {
	var result ownedMP3Header
	if len(data) < 4 {
		return result, false
	}
	header := binary.BigEndian.Uint32(data)
	// MPEG1, LayerIII, no frame CRC, CBR192k; emphasis/unknown modes fallback.
	if header&0xffe00000 != 0xffe00000 || (header>>19)&3 != 3 || (header>>17)&3 != 1 || (header>>16)&1 != 1 || (header>>12)&15 != 11 || (header>>10)&3 == 3 || header&3 != 0 {
		return result, false
	}
	rate := [...]int{44100, 48000, 32000}[(header>>10)&3]
	channels := 2
	if (header>>6)&3 == 3 {
		channels = 1
	}
	return ownedMP3Header{bytes: 144*192000/rate + int((header>>9)&1), rate: rate, channels: channels}, true
}

// FFmpeg's MP3 muxer records CRC16/ANSI-LE of every audio packet, excluding
// the Info frame. Validate it while scanning, so payload mutation also falls
// back; MPEG frame CRC remains disabled by the established libmp3lame output.
var ownedMP3CRCTable = func() (table [256]uint16) {
	for i := range table {
		value := uint16(i)
		for range 8 {
			if value&1 != 0 {
				value = value>>1 ^ 0xa001
			} else {
				value >>= 1
			}
		}
		table[i] = value
	}
	return
}()

func ownedMP3CRC(value uint16, data []byte) uint16 {
	for _, b := range data {
		value = value>>8 ^ ownedMP3CRCTable[byte(value)^b]
	}
	return value
}
