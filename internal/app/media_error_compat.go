package app

import (
	"fmt"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

// localMediaError translates only the backend's direct diagnostic types.
// Existing app errors and opaque callback causes retain their original identity.
func localMediaError(err error) error {
	switch value := err.(type) {
	case *mediaerror.Problem:
		switch value {
		case mediaerror.ErrNoAudio:
			return errNoAudio
		case mediaerror.ErrOutputLimit:
			return errOutputLimit
		case mediaerror.ErrUnsupportedStream:
			return errUnsupportedStream
		}
		return &sourceProblem{code: value.Code, message: value.Message}
	case *mediaerror.ProcessFailure:
		return &mediaProcessFailure{cause: localMediaError(value.Cause), category: value.Category}
	case *mediaerror.InspectionFailure:
		return &localInspectionFailure{cause: localMediaError(value.Cause)}
	case *mediaerror.CopyFailure:
		problem := localMediaError(value.Problem).(*sourceProblem)
		return &copyExportFailure{problem: problem, cause: localMediaError(value.Cause)}
	case *mediaerror.RenderFailure:
		return fmt.Errorf("%w: %w", localMediaError(value.Problem), localMediaError(value.Cause))
	}
	if err == mediaerror.ErrProcessResponseTooLarge {
		return errProcessResponseTooLarge
	}
	return err
}

// These projections let neutral classifiers inspect legacy app errors without
// changing their concrete app types, private fields, messages or unwrap chains.
func (e *sourceProblem) As(target any) bool {
	if result, ok := target.(**mediaerror.Problem); ok {
		*result = &mediaerror.Problem{Code: e.code, Message: e.message}
		return true
	}
	return false
}

func (e *mediaProcessFailure) As(target any) bool {
	if result, ok := target.(**mediaerror.ProcessFailure); ok {
		*result = &mediaerror.ProcessFailure{Cause: e.cause, Category: e.category}
		return true
	}
	return false
}

func (e *localInspectionFailure) As(target any) bool {
	if result, ok := target.(**mediaerror.InspectionFailure); ok {
		*result = &mediaerror.InspectionFailure{Cause: e.cause}
		return true
	}
	return false
}

func (e *copyExportFailure) As(target any) bool {
	if result, ok := target.(**mediaerror.CopyFailure); ok {
		*result = &mediaerror.CopyFailure{Problem: &mediaerror.Problem{Code: e.problem.code, Message: e.problem.message}, Cause: e.cause}
		return true
	}
	return false
}
