package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

func TestUnsupportedPublicationFailsBeforeEncoding(t *testing.T) {
	input := fixture(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "result.mp4")
	e := New(Config{FFmpegPath: filepath.Join(dir, "must-not-be-invoked")})
	e.preflight = func(string, string) error { return fsdurable.ErrUnsupportedFilesystem }
	_, err := e.Export(context.Background(), input, output, Range{EndMS: 1000}, Options{})
	if !errors.Is(err, ErrFilesystemUnsupported) || CodeOf(err) != CodeFilesystemUnsupported {
		t.Fatal("filesystem failure was discovered after encoding or lost its stable code", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed preflight leaked an output or workspace", entries, err)
	}
}

func TestEngineSynchronizationFailureDoesNotReturnSuccess(t *testing.T) {
	input := fixture(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "result.mp4")
	e := New(Config{FFmpegThreads: 1})
	e.publish = func(context.Context, string, string) (fs.FileInfo, error) {
		return nil, errors.Join(fsdurable.ErrSyncFailed, syscall.EIO)
	}
	result, err := e.Export(context.Background(), input, output, Range{EndMS: 1000}, Options{})
	if !errors.Is(err, ErrOutputSyncFailed) || !errors.Is(err, syscall.EIO) || CodeOf(err) != CodeOutputSyncFailed || result != (Result{}) {
		t.Fatal("synchronization failure returned success or lost its cause", result, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed synchronization leaked output/workspace", entries, err)
	}
}

func TestEngineUncertainPublicationRetainsCompleteOutput(t *testing.T) {
	input := fixture(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "result.mp4")
	e := New(Config{FFmpegThreads: 1})
	e.publish = func(ctx context.Context, source, destination string) (fs.FileInfo, error) {
		if _, err := fsdurable.Publish(ctx, source, destination); err != nil {
			return nil, err
		}
		return nil, errors.Join(fsdurable.ErrPublicationUncertain, syscall.EIO)
	}
	result, err := e.Export(context.Background(), input, output, Range{EndMS: 1000}, Options{})
	if CodeOf(err) != CodePublicationUncertain || !errors.Is(err, ErrPublicationUncertain) || result != (Result{}) {
		t.Fatal("uncertain publication was reported as ordinary failure or success", result, err)
	}
	if info, err := e.Inspect(context.Background(), output); err != nil || info.DurationMS < 900 {
		t.Fatal("uncertain published output was removed or incomplete", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "result.mp4" {
		t.Fatal("uncertain publication lost ownership or leaked its workspace", entries, err)
	}
}
