package app

import (
	"math"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
)

// Reservations follow the selected duration, with the very same byte ceiling
// enforced by the producer. A ten-second clip must not reserve a 16 GiB file.
// Allow container headers, decoder context and the final muxer's metadata.
const exportContainerAllowance int64 = mediaexport.ContainerAllowance

const subtitleTrackAllowance int64 = maxMediaStreams * (8 << 20)

// One extracted source copy and one rebased output copy can coexist per track.
const subtitleStageAllowance int64 = 2 * subtitleTrackAllowance

func boundedMediaBytes(durationMS, bitsPerSecond, allowance, limit int64) int64 {
	return mediaexport.BoundedMediaBytes(durationMS, bitsPerSecond, allowance, limit)
}

func exportVideoBitrate(c Config, r Range, request ExportRequest) int64 {
	return mediaexport.VideoBitrate(mediaExportConfig(c), r, edit.ExportRequest(request))
}

func outputBudget(c Config, r Range, request ExportRequest) int64 {
	return mediaexport.OutputBudget(mediaExportConfig(c), r, edit.ExportRequest(request))
}

func exportTransientBudget(c Config, r Range, request ExportRequest) int64 {
	return mediaexport.TransientBudget(mediaExportConfig(c), r, edit.ExportRequest(request))
}

func joinedOutputRange(request ExportRequest) Range {
	return mediaexport.JoinedOutputRange(edit.ExportRequest(request))
}

func joinedOutputBudget(c Config, request ExportRequest) int64 {
	return mediaexport.JoinedOutputBudget(mediaExportConfig(c), edit.ExportRequest(request))
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
	transient := exportTransientBudget(c, joinedOutputRange(request), request)
	if transient < 0 || transient > math.MaxInt64-size {
		return 0, errStorageUnavailable
	}
	size += transient
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
