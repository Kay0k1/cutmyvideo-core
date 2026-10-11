package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

// runCLI keeps successful stdout stable and sends all diagnostics to stderr.
// --json-errors is global and must precede the command. Flag's own error/usage
// output is buffered so local/argument failures contain one JSON object only.
// Services retain their operational stderr logs before any terminal diagnostic.
func runCLI(args []string, stdout, stderr io.Writer) int {
	jsonErrors := len(args) > 0 && args[0] == "--json-errors"
	var flagOutput bytes.Buffer
	diagnostics := stderr
	if jsonErrors {
		args = args[1:]
		diagnostics = &flagOutput
	}
	err := runArgs(args, stdout, diagnostics)
	if err == nil {
		if jsonErrors {
			_, _ = io.Copy(stderr, &flagOutput)
		}
		return 0
	}
	code := engine.CodeOf(err)
	if jsonErrors {
		_ = json.NewEncoder(stderr).Encode(struct {
			Error struct {
				Code    engine.ErrorCode `json:"code"`
				Message string           `json:"message"`
			} `json:"error"`
		}{Error: struct {
			Code    engine.ErrorCode `json:"code"`
			Message string           `json:"message"`
		}{code, err.Error()}})
	} else if stderr == os.Stderr {
		slog.Error("cutmy stopped", "error", err)
	} else {
		slog.New(slog.NewTextHandler(stderr, nil)).Error("cutmy stopped", "error", err)
	}
	return exitCode(code)
}

func exitCode(code engine.ErrorCode) int {
	switch code {
	case engine.CodeInvalidArgument:
		return 2
	case engine.CodeTimeout:
		return 124
	case engine.CodeCancelled:
		return 130
	default:
		return 1
	}
}

func invalidCLI(err error) error { return &engine.Error{Code: engine.CodeInvalidArgument, Err: err} }

// Keep startup diagnostics redacted while retaining cancellation/deadline
// causes for the same exit-code contract as local operations.
type redactedCLIError struct {
	message string
	cause   error
}

func (e *redactedCLIError) Error() string { return e.message }
func (e *redactedCLIError) Unwrap() error { return e.cause }
