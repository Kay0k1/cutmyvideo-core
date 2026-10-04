package app

import (
	"context"
	"errors"
)

var errNoAudio = &sourceProblem{"audio_missing", "This video has no audio track. Export it as MP4 video instead"}
var errOutputLimit = &sourceProblem{"output_limit", "The result exceeds the output size limit. Choose a shorter fragment or lower quality"}

// Export diagnostics are a small public vocabulary, never raw subprocess or
// network errors. Older clients can still use the accompanying fixed message.
func exportProblem(err error) *sourceProblem {
	if p := problemFromError(err); p != nil {
		return p
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &sourceProblem{"job_timeout", "Processing exceeded the time limit. Choose a shorter fragment or lower quality"}
	}
	var process *mediaProcessFailure
	if errors.As(err, &process) {
		switch process.category {
		case "missing_audio":
			return errNoAudio
		case "storage_full":
			return &sourceProblem{"storage_full", "Server storage is full; try again after older files expire"}
		case "copy_incompatible":
			return &sourceProblem{"copy_incompatible", "These source codecs cannot be copied into MP4. Choose accurate mode"}
		case "platform_access":
			return errPlatformAccess
		case "upstream_denied":
			return &sourceProblem{"media_upstream_denied", "The platform refused access to the media stream. Open the video again or upload your file"}
		case "upstream_unavailable":
			return &sourceProblem{"media_upstream_unavailable", "The media stream is temporarily unavailable. Try exporting again"}
		case "network_timeout":
			return &sourceProblem{"media_network_timeout", "The media stream did not respond in time. Try exporting again"}
		case "network_reset":
			return &sourceProblem{"media_network_reset", "The platform interrupted the media transfer. Try exporting again"}
		case "unsupported_media":
			return errUnsupportedStream
		}
	}
	return &sourceProblem{"media_processing_failed", "Could not export this fragment. Try exporting again or upload the video file"}
}
