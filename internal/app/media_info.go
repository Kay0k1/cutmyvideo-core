package app

import "github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"

// Keep the service's existing diagnostics and sentinel identity at its boundary.
// Ordinary validation, cancellation and OS errors retain their original causes.
func mediaInfoError(err error) error {
	if err == mediainfo.ErrUnsupported {
		return errUnsupportedStream
	}
	if problem, ok := err.(*mediainfo.Problem); ok {
		return &sourceProblem{code: problem.Code, message: problem.Message}
	}
	return err
}
