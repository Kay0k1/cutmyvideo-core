package mediaexport

import (
	"math"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

// Reservations follow the selected duration, with the very same byte ceiling
// enforced by the producer. A ten-second clip must not reserve a 16 GiB file.
// Allow container headers, decoder context and the final muxer's metadata.
const ContainerAllowance int64 = 16 << 20

func BoundedMediaBytes(durationMS, bitsPerSecond, allowance, limit int64) int64 {
	if limit <= 0 || durationMS <= 0 || bitsPerSecond <= 0 {
		return limit
	}
	if durationMS > math.MaxInt64/bitsPerSecond {
		return limit
	}
	bytes := durationMS * bitsPerSecond / 8000
	if bytes > math.MaxInt64-allowance {
		return limit
	}
	return min(limit, bytes+allowance)
}

func VideoBitrate(c Config, r edit.Range, request edit.ExportRequest) int64 {
	bitrate := int64(32_000_000)
	if request.Quality == "720p" {
		bitrate = 5_000_000
	} else if request.Quality == "1080p" {
		bitrate = 8_000_000
	}
	// Long accurate exports adapt the bitrate to the file limit up front,
	// instead of spending hours encoding and then truncating at that limit.
	duration := r.EndMS - r.StartMS
	if duration >= 600_000 && c.MaxOutputBytes > ContainerAllowance && c.MaxOutputBytes <= math.MaxInt64/7200 {
		available := (c.MaxOutputBytes - ContainerAllowance) * 7200 / duration
		bitrate = min(bitrate, max(256_000, available-256_000))
	}
	return bitrate
}

func OutputBudget(c Config, r edit.Range, request edit.ExportRequest) int64 {
	if request.Format == "" || r.EndMS <= r.StartMS {
		return c.MaxOutputBytes // Older snapshots and migration/test callers.
	}
	duration := r.EndMS - r.StartMS
	rate := int64(224_000)
	if request.Format != "mp3" {
		if request.CutMode == "copy" {
			// Original files can be much denser than streaming video. This is a
			// generous 1 Gb/s hard ceiling, including up to 30s keyframe
			// preroll. This avoids reserving 16 GiB for every tiny copy while
			// accommodating high-bitrate camera originals.
			rate = 1_000_000_000
			duration += min(int64(30_000), r.StartMS)
		} else {
			rate = (VideoBitrate(c, r, request) + 256_000) * 11 / 10
		}
	}
	return BoundedMediaBytes(duration, rate, ContainerAllowance, c.MaxOutputBytes)
}

func JoinedOutputRange(request edit.ExportRequest) edit.Range {
	var duration int64
	for _, selected := range request.Ranges {
		duration += selected.EndMS - selected.StartMS
	}
	return edit.Range{EndMS: duration}
}

func JoinedOutputBudget(c Config, request edit.ExportRequest) int64 {
	outputRange := JoinedOutputRange(request)
	if request.CutMode == "copy" {
		for _, selected := range request.Ranges {
			preroll := min(int64(30_000), selected.StartMS)
			if preroll > math.MaxInt64-outputRange.EndMS {
				return c.MaxOutputBytes
			}
			outputRange.EndMS += preroll
		}
	}
	return OutputBudget(c, outputRange, request)
}
