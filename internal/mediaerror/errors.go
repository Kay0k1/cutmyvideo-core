// Package mediaerror owns portable media diagnostics and their underlying causes.
package mediaerror

import (
	"errors"
	"fmt"
)

type Problem struct{ Code, Message string }

func (e *Problem) Error() string { return e.Message }

type ProcessFailure struct {
	Cause    error
	Category string
}

func (e *ProcessFailure) Error() string {
	return fmt.Sprintf("media process failed [%s]: %v", e.Category, e.Cause)
}
func (e *ProcessFailure) Unwrap() error { return e.Cause }

type InspectionFailure struct{ Cause error }

func (e *InspectionFailure) Error() string { return e.Cause.Error() }
func (e *InspectionFailure) Unwrap() error { return e.Cause }

var ErrNoAudio = &Problem{Code: "audio_missing", Message: "This video has no audio track. Export it as MP4 video instead"}
var ErrOutputLimit = &Problem{Code: "output_limit", Message: "The result exceeds the output size limit. Choose a shorter fragment or lower quality"}
var ErrUnsupportedStream = &Problem{Code: "unsupported_stream", Message: "This video uses unsupported encryption or media streams. Upload an accessible file instead"}
var ErrProcessResponseTooLarge = errors.New("process response is too large")

type CopyFailure struct {
	Problem *Problem
	Cause   error
}

func (e *CopyFailure) Error() string   { return e.Problem.Error() }
func (e *CopyFailure) Unwrap() []error { return []error{e.Problem, e.Cause} }

func CopyDiagnostic(err error, format string) error {
	var existing *CopyFailure
	if errors.As(err, &existing) {
		return err
	}
	var process *ProcessFailure
	if errors.As(err, &process) && process.Category == "copy_incompatible" {
		problem := &Problem{Code: "copy_incompatible", Message: "Selected streams cannot be copied into this container. Choose another compatible container, or accurate MP4 to encode"}
		if format == "mp4" {
			problem.Message = "Selected streams cannot be copied into MP4. Choose MKV to preserve them, or accurate MP4 to encode"
		}
		if format == "mkv" {
			problem.Message = "Selected streams cannot be copied into MKV. Choose accurate MP4 to encode"
		}
		return &CopyFailure{Problem: problem, Cause: err}
	}
	return err
}

// RenderFailure retains the exact two-cause shape used for classified HDR
// failures, so compatibility adapters can reconstruct the legacy wrapper.
type RenderFailure struct {
	Problem *Problem
	Cause   error
}

func (e *RenderFailure) Error() string {
	return fmt.Sprintf("%s: %v", e.Problem, e.Cause)
}
func (e *RenderFailure) Unwrap() []error { return []error{e.Problem, e.Cause} }

func RenderedFailure(err error, hdr bool) error {
	var process *ProcessFailure
	if hdr && errors.As(err, &process) && process.Category == "hdr_conversion_unavailable" {
		return &RenderFailure{Problem: &Problem{Code: "hdr_conversion_unavailable", Message: "HDR conversion to SDR is unavailable with the configured media tools"}, Cause: err}
	}
	return err
}
