package app

import "github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"

// LocalErrorCode shares fixed media diagnostics with the local public engine.
// It never inspects human error text or exposes subprocess diagnostics.
func LocalErrorCode(err error) string { return mediaerror.LocalErrorCode(err) }

type localInspectionFailure struct{ cause error }

func (e *localInspectionFailure) Error() string { return e.cause.Error() }
func (e *localInspectionFailure) Unwrap() error { return e.cause }
