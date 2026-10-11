package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPublicErrorCodesPreserveCauses(t *testing.T) {
	permission := &os.PathError{Op: "open", Path: "private", Err: os.ErrPermission}
	cases := []struct {
		name  string
		err   error
		code  ErrorCode
		cause error
	}{
		{"cancel", fmt.Errorf("wrapped: %w", context.Canceled), CodeCancelled, context.Canceled},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), CodeTimeout, context.DeadlineExceeded},
		{"permission", permission, CodePermissionDenied, os.ErrPermission},
		{"storage", fmt.Errorf("wrapped: %w", syscall.ENOSPC), CodeStorageFull, syscall.ENOSPC},
		{"quota", errors.Join(ErrOutputSyncFailed, syscall.EDQUOT), CodeStorageFull, syscall.EDQUOT},
		{"missing-tool", &exec.Error{Name: "absent-tool", Err: exec.ErrNotFound}, CodeToolUnavailable, exec.ErrNotFound},
		{"exists", ErrOutputExists, CodeOutputExists, ErrOutputExists},
		{"schema", fmt.Errorf("wrapped: %w", ErrSchemaIncompatible), CodeSchemaIncompatible, ErrSchemaIncompatible},
		{"initializing", fmt.Errorf("wrapped: %w", ErrStorageInitializing), CodeStorageInitializing, ErrStorageInitializing},
		{"initializing-cancelled", errors.Join(context.Canceled, ErrStorageInitializing), CodeCancelled, context.Canceled},
		{"initializing-deadline", errors.Join(context.DeadlineExceeded, ErrStorageInitializing), CodeTimeout, context.DeadlineExceeded},
		{"uncertain-wins", errors.Join(ErrPublicationUncertain, context.Canceled, syscall.ENOSPC), CodePublicationUncertain, context.Canceled},
		{"unsupported-wins", errors.Join(ErrFilesystemUnsupported, ErrPublicationFailed), CodeFilesystemUnsupported, ErrFilesystemUnsupported},
		{"sync-storage", errors.Join(ErrOutputSyncFailed, syscall.ENOSPC), CodeStorageFull, syscall.ENOSPC},
		{"sync-permission", errors.Join(ErrOutputSyncFailed, permission), CodePermissionDenied, os.ErrPermission},
		{"unknown", errors.New("an arbitrary error"), CodeProcessingFailed, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeError(tc.err)
			var public *Error
			if !errors.As(got, &public) || CodeOf(got) != tc.code || got.Error() != tc.err.Error() {
				t.Fatalf("classification lost code/text: %T %v", got, got)
			}
			if tc.cause != nil && !errors.Is(got, tc.cause) {
				t.Fatalf("lost underlying cause: %v", got)
			}
			if normalizeError(got) != got {
				t.Fatal("double normalization changed the public error")
			}
		})
	}
	if CodeOf(nil) != "" {
		t.Fatal("nil should have no code")
	}
	future := &Error{Code: "future_code", Err: context.Canceled}
	if CodeOf(future) != "future_code" || !errors.Is(future, context.Canceled) {
		t.Fatal("unknown public code or cause was discarded")
	}
}

func TestPublicInspectErrors(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		config     Config
		code       ErrorCode
		cause      error
	}{
		{"config", "unused", Config{FFmpegThreads: -1}, CodeInvalidArgument, nil},
		{"empty", "", Config{}, CodeInvalidArgument, nil},
		{"missing", filepath.Join(t.TempDir(), "absent"), Config{}, CodeInputNotFound, os.ErrNotExist},
		{"directory", t.TempDir(), Config{}, CodeInvalidArgument, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.config).Inspect(context.Background(), tc.path)
			if CodeOf(err) != tc.code || tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("inspect: code=%s, err=%v", CodeOf(err), err)
			}
			var public *Error
			if !errors.As(err, &public) {
				t.Fatal("operation did not return a public Error")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("not media"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{FFprobePath: filepath.Join(t.TempDir(), "missing-probe")}).Inspect(context.Background(), path)
	// Windows resolves even absolute command names through LookPath, whose
	// missing executable cause is exec.ErrNotFound. Preserve the OS's cause.
	if CodeOf(err) != CodeToolUnavailable || (!errors.Is(err, os.ErrNotExist) && !errors.Is(err, exec.ErrNotFound)) {
		t.Fatalf("missing tool code/cause: %v", err)
	}
}

func TestOutputSetupFailureIsNotMissingInput(t *testing.T) {
	input := fixture(t)
	output := filepath.Join(t.TempDir(), "missing-parent", "output.mp4")
	_, err := New(Config{}).Export(context.Background(), input, output, Range{EndMS: 1000}, Options{})
	if CodeOf(err) != CodePublicationFailed || !errors.Is(err, os.ErrNotExist) || !errors.Is(err, ErrPublicationFailed) {
		t.Fatalf("output failure classified as input: code=%s err=%v", CodeOf(err), err)
	}
}
