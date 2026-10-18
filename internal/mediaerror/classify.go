package mediaerror

import "errors"

// LocalErrorCode never reads human error text or exposes subprocess diagnostics.
// Problem, process and inspection precedence matches the legacy app classifier.
func LocalErrorCode(err error) string {
	var problem *Problem
	if errors.As(err, &problem) {
		switch problem.Code {
		case "crop_invalid":
			return "invalid_argument"
		case "invalid_argument", "source_changed", "tool_unavailable", "encoder_unavailable", "filter_unavailable", "muxer_unavailable", "demuxer_unavailable", "bitstream_filter_unavailable", "audio_missing", "output_limit", "copy_incompatible", "copy_quality_unsupported", "hdr_conversion_unavailable", "audio_track_unavailable", "subtitle_unsupported", "subtitle_unavailable", "join_incompatible", "target_size_unattainable", "copy_audio_unsupported":
			return problem.Code
		case "unsupported_stream":
			return "unsupported_media"
		}
	}
	var process *ProcessFailure
	if errors.As(err, &process) {
		switch process.Category {
		case "missing_audio":
			return "audio_missing"
		case "storage_full", "copy_incompatible", "unsupported_media", "hdr_conversion_unavailable":
			return process.Category
		default:
			return "processing_failed"
		}
	}
	var inspection *InspectionFailure
	if errors.As(err, &inspection) {
		return "unsupported_media"
	}
	return ""
}
