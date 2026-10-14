package app

import "errors"

// LocalErrorCode shares fixed media diagnostics with the local public engine.
// It never inspects human error text or exposes subprocess diagnostics.
func LocalErrorCode(err error) string {
	var problem *sourceProblem
	if errors.As(err, &problem) {
		switch problem.code {
		case "crop_invalid":
			return "invalid_argument"
		case "invalid_argument", "source_changed", "tool_unavailable", "encoder_unavailable", "filter_unavailable", "muxer_unavailable", "demuxer_unavailable", "bitstream_filter_unavailable", "audio_missing", "output_limit", "copy_incompatible", "copy_quality_unsupported", "hdr_conversion_unavailable", "audio_track_unavailable", "subtitle_unsupported", "subtitle_unavailable", "join_incompatible":
			return problem.code
		case "unsupported_stream":
			return "unsupported_media"
		}
	}
	var process *mediaProcessFailure
	if errors.As(err, &process) {
		switch process.category {
		case "missing_audio":
			return "audio_missing"
		case "storage_full", "copy_incompatible", "unsupported_media", "hdr_conversion_unavailable":
			return process.category
		default:
			return "processing_failed"
		}
	}
	var inspection *localInspectionFailure
	if errors.As(err, &inspection) {
		return "unsupported_media"
	}
	return ""
}

type localInspectionFailure struct{ cause error }

func (e *localInspectionFailure) Error() string { return e.cause.Error() }
func (e *localInspectionFailure) Unwrap() error { return e.cause }
