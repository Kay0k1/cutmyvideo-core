package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

var (
	// ErrFilesystemUnsupported reports a missing required hard-link/directory-
	// sync capability. Export checks these capabilities before encoding.
	ErrFilesystemUnsupported = fsdurable.ErrUnsupportedFilesystem
	// ErrOutputSyncFailed means the completed output/probe could not be synced.
	ErrOutputSyncFailed = fsdurable.ErrSyncFailed
	// ErrPublicationFailed means publication failed with no uncertain result.
	ErrPublicationFailed = fsdurable.ErrPublicationFailed
	// ErrPublicationUncertain means the final path may exist after a failed
	// publication/cleanup sync. Inspect that path before deciding to retry.
	ErrPublicationUncertain = fsdurable.ErrPublicationUncertain
)

// ErrorCode is a stable machine-readable diagnostic. Consumers must allow
// unknown future codes and use a generic failure message for them.
type ErrorCode string

const (
	CodeInvalidArgument        ErrorCode = "invalid_argument"
	CodeInputNotFound          ErrorCode = "input_not_found"
	CodePermissionDenied       ErrorCode = "permission_denied"
	CodeToolUnavailable        ErrorCode = "tool_unavailable"
	CodeUnsupportedMedia       ErrorCode = "unsupported_media"
	CodeAudioMissing           ErrorCode = "audio_missing"
	CodeOutputLimit            ErrorCode = "output_limit"
	CodeCopyIncompatible       ErrorCode = "copy_incompatible"
	CodeCopyQualityUnsupported ErrorCode = "copy_quality_unsupported"
	CodeStorageFull            ErrorCode = "storage_full"
	CodeTimeout                ErrorCode = "timeout"
	CodeCancelled              ErrorCode = "cancelled"
	CodeOutputExists           ErrorCode = "output_exists"
	CodeFilesystemUnsupported  ErrorCode = "filesystem_unsupported"
	CodeOutputSyncFailed       ErrorCode = "output_sync_failed"
	CodePublicationFailed      ErrorCode = "publication_failed"
	CodePublicationUncertain   ErrorCode = "publication_uncertain"
	CodeSchemaIncompatible     ErrorCode = "schema_incompatible"
	CodeProcessingFailed       ErrorCode = "processing_failed"
)

// Error describes an operation failure without losing its underlying cause.
// Code is the contract; Err.Error() is a human diagnostic and may change.
// Use errors.As to inspect Error and errors.Is for context or operating-system
// causes. A nil Err is permitted and renders the code itself.
type Error struct {
	Code ErrorCode
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Code)
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the first public Error's code, or classifies a standard cause.
// It returns an empty code for nil and CodeProcessingFailed for unknown errors.
// Codes describe failures; they do not authorize a blind retry, especially when
// CodePublicationUncertain means an output may already exist.
func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var public *Error
	if errors.As(err, &public) {
		return public.Code
	}
	if errors.Is(err, ErrPublicationUncertain) {
		return CodePublicationUncertain
	}
	if errors.Is(err, ErrFilesystemUnsupported) {
		return CodeFilesystemUnsupported
	}
	if errors.Is(err, app.ErrSchemaIncompatible) {
		return CodeSchemaIncompatible
	}
	if errors.Is(err, context.Canceled) {
		return CodeCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}
	if errors.Is(err, ErrOutputExists) {
		return CodeOutputExists
	}
	var tool *exec.Error
	if errors.As(err, &tool) {
		return CodeToolUnavailable
	}
	var path *os.PathError
	if errors.As(err, &path) && path.Op == "fork/exec" && errors.Is(err, os.ErrNotExist) {
		return CodeToolUnavailable
	}
	if errors.Is(err, os.ErrPermission) {
		return CodePermissionDenied
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return CodeStorageFull
	}
	if errors.Is(err, ErrPublicationFailed) {
		return CodePublicationFailed
	}
	if errors.Is(err, ErrOutputSyncFailed) {
		return CodeOutputSyncFailed
	}
	if errors.Is(err, os.ErrNotExist) {
		return CodeInputNotFound
	}
	if code := app.LocalErrorCode(err); code != "" {
		return ErrorCode(code)
	}
	return CodeProcessingFailed
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var public *Error
	if errors.As(err, &public) {
		return err
	}
	return &Error{Code: CodeOf(err), Err: err}
}

func invalidArgument(err error) error { return &Error{Code: CodeInvalidArgument, Err: err} }

// messageCause preserves an established human diagnostic and an OS cause.
type messageCause struct {
	message string
	cause   error
}

func (e *messageCause) Error() string { return e.message }
func (e *messageCause) Unwrap() error { return e.cause }
