package app

import "math"

// Reservations follow the selected duration, with the very same byte ceiling
// enforced by the producer. A ten-second clip must not reserve a 16 GiB file.
// Allow container headers, decoder context and the final muxer's metadata.
const exportContainerAllowance int64 = 16 << 20

func boundedMediaBytes(durationMS, bitsPerSecond, allowance, limit int64) int64 {
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

func exportVideoBitrate(c Config, r Range, request ExportRequest) int64 {
	bitrate := int64(32_000_000)
	if request.Quality == "720p" {
		bitrate = 5_000_000
	} else if request.Quality == "1080p" {
		bitrate = 8_000_000
	}
	// Long accurate exports adapt the bitrate to the file limit up front,
	// instead of spending hours encoding and then truncating at that limit.
	duration := r.EndMS - r.StartMS
	if duration >= 600_000 && c.MaxOutputBytes > exportContainerAllowance && c.MaxOutputBytes <= math.MaxInt64/7200 {
		available := (c.MaxOutputBytes - exportContainerAllowance) * 7200 / duration
		bitrate = min(bitrate, max(256_000, available-256_000))
	}
	return bitrate
}

func outputBudget(c Config, r Range, request ExportRequest) int64 {
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
			rate = (exportVideoBitrate(c, r, request) + 256_000) * 11 / 10
		}
	}
	return boundedMediaBytes(duration, rate, exportContainerAllowance, c.MaxOutputBytes)
}

func inputBudget(c Config, r Range, request ExportRequest) int64 {
	if request.Format == "" || r.EndMS <= r.StartMS {
		return remoteSourceBudget(c)
	}
	rate := int64(100_000_000)
	if request.Quality == "720p" {
		rate = 24_000_000
	} else if request.Quality == "1080p" {
		rate = 40_000_000
	}
	// Keep one minute of segment/keyframe context and a fixed staging allowance.
	// The stage writer enforces this ceiling even for unusually dense segments.
	return boundedMediaBytes(r.EndMS-r.StartMS+60_000, rate, 128<<20, remoteSourceBudget(c))
}
