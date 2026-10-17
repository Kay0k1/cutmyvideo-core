package mediaexport

import (
	"math"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func SameJoinedStream(a, b *mediainfo.Stream) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Index == b.Index && a.CodecName == b.CodecName && a.TimeBase == b.TimeBase && a.ExtradataHash == b.ExtradataHash && a.Width == b.Width && a.Height == b.Height && a.SampleRate == b.SampleRate && a.Channels == b.Channels && a.SampleAspectRatio == b.SampleAspectRatio && a.Rotation() == b.Rotation()
}

func FFConcatFile(path string) string {
	return "file '" + strings.ReplaceAll(path, "'", "'\\''") + "'\n"
}

func CopyAudioPacketTrimEligible(selection Selection, request edit.ExportRequest) bool {
	if selection.Audio == nil || request.Format != "mkv" {
		return false
	}
	// The established audio.mka stage normalizes packet clocks to milliseconds.
	// Bypassing it for a finer source clock changes copied PTS/durations even
	// when payloads and decoded samples agree. Keep that stage unless the source
	// already uses its time base and the output keeps it too. Older MP4 muxers
	// also adjust packet durations after that stage. Exact packet-clock witnesses
	// cover only the Matroska-to-Matroska AAC/MP3/FLAC subset.
	parts := strings.Split(selection.Audio.TimeBase, "/")
	if len(parts) != 2 {
		return false
	}
	numerator, numeratorErr := strconv.ParseInt(parts[0], 10, 32)
	denominator, denominatorErr := strconv.ParseInt(parts[1], 10, 32)
	if numeratorErr != nil || denominatorErr != nil || numerator <= 0 || denominator <= 0 || numerator*1000 != denominator {
		return false
	}
	clock, err := strconv.ParseFloat(selection.Audio.StartTime, 64)
	if err != nil || math.IsNaN(clock) || math.IsInf(clock, 0) {
		return false
	}
	switch selection.Audio.CodecName {
	case "aac", "mp3", "flac":
		return true
	default:
		// Preserve the established path until the codec's decoder-delay and
		// container clocks have an independent packet and decoded-audio witness.
		return false
	}
}
