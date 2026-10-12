package app

import "math"

// Reservations follow the selected duration, with the very same byte ceiling
// enforced by the producer. A ten-second clip must not reserve a 16 GiB file.
// Allow container headers, decoder context and the final muxer's metadata.
const exportContainerAllowance int64 = 16 << 20

const subtitleTrackAllowance int64 = maxMediaStreams * (8 << 20)

// One extracted source copy and one rebased output copy can coexist per track.
const subtitleStageAllowance int64 = 2 * subtitleTrackAllowance

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

func joinedOutputRange(request ExportRequest) Range {
	var duration int64
	for _, selected := range request.Ranges {
		duration += selected.EndMS - selected.StartMS
	}
	return Range{EndMS: duration}
}

func joinedOutputBudget(c Config, request ExportRequest) int64 {
	outputRange := joinedOutputRange(request)
	if request.CutMode == "copy" {
		for _, selected := range request.Ranges {
			preroll := min(int64(30_000), selected.StartMS)
			if preroll > math.MaxInt64-outputRange.EndMS {
				return c.MaxOutputBytes
			}
			outputRange.EndMS += preroll
		}
	}
	return outputBudget(c, outputRange, request)
}

func subtitleWorkBudget(c Config, request ExportRequest, output int64) (int64, error) {
	return subtitleStageBudget(request, output, subtitleStageAllowance)
}

func subtitleStageBudget(request ExportRequest, output, textAllowance int64) (int64, error) {
	if request.SubtitleMode != "preserve" {
		return 0, nil
	}
	// The preserved text and subtitle-free media coexist until the final
	// mux passes verification. Text extraction has a bounded stream ceiling.
	if output > math.MaxInt64-textAllowance {
		return 0, errStorageUnavailable
	}
	return output + textAllowance, nil
}

// Accurate joins decode from a concat plan into one output and do not produce
// encoded segment files. Copy joins retain their actual intermediate segments.
// Remote HLS stages coexist because each range has its own source offset.
func joinedWorkBudget(c Config, request ExportRequest, platform bool) (int64, error) {
	size := joinedOutputBudget(c, request)
	if size <= 0 {
		return 0, errStorageUnavailable
	}
	subtitles, err := subtitleWorkBudget(c, request, size)
	if err != nil || subtitles > math.MaxInt64-size {
		return 0, errStorageUnavailable
	}
	size += subtitles
	var copyTransient int64
	for _, selected := range request.Ranges {
		if request.CutMode == "copy" {
			budget := outputBudget(c, selected, request)
			copyTransient = max(copyTransient, budget)
			if budget <= 0 || budget > math.MaxInt64-size {
				return 0, errStorageUnavailable
			}
			size += budget
		}
		if platform {
			staging := inputBudget(c, selected, request)
			if staging < 0 || staging > (math.MaxInt64-size)/2 {
				return 0, errStorageUnavailable
			}
			size += 2 * staging
		}
	}
	// Copy joins align packet audio with the measured video boundary through
	// a sequential video/audio remux. Those producers coexist with one segment
	// output; a single largest-segment ceiling covers that temporary peak.
	if copyTransient > math.MaxInt64-size {
		return 0, errStorageUnavailable
	}
	size += copyTransient
	return size, nil
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
