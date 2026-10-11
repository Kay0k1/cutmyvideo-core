package fsdurable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

func publicationFiles(t *testing.T) (string, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "completed")
	destination := filepath.Join(t.TempDir(), "published")
	if err := os.WriteFile(source, []byte("complete result"), 0600); err != nil {
		t.Fatal(err)
	}
	return source, destination
}

func requirePublicationSupport(t *testing.T, source, destination string) {
	t.Helper()
	if err := Preflight(filepath.Dir(source), filepath.Dir(destination)); err != nil {
		if errors.Is(err, ErrUnsupportedFilesystem) && os.Getenv("CUTMY_REQUIRE_DURABLE_FS") != "1" {
			t.Skip("filesystem lacks required durable exclusive publication capabilities")
		}
		t.Fatal(err)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected published name %q: %v", path, err)
	}
}

func TestFileSyncFailureNeverMakesOutputVisible(t *testing.T) {
	source, destination := publicationFiles(t)
	ops := systemOperations()
	ops.syncFile = func(*os.File) error { return syscall.EIO }
	ops.link = func(string, string) error {
		t.Fatal("publication ran before successful data synchronization")
		return nil
	}
	if _, err := publish(context.Background(), source, destination, ops); !errors.Is(err, ErrSyncFailed) || !errors.Is(err, syscall.EIO) {
		t.Fatal("lost prepublication synchronization error", err)
	}
	assertAbsent(t, destination)
	if data, err := os.ReadFile(source); err != nil || string(data) != "complete result" {
		t.Fatal("failed synchronization changed the staging file", err)
	}
}

func TestCancellationDuringSyncStopsBeforeLink(t *testing.T) {
	source, destination := publicationFiles(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := systemOperations()
	ops.syncFile = func(file *os.File) error {
		if err := file.Sync(); err != nil {
			return err
		}
		cancel()
		return nil
	}
	if _, err := publish(ctx, source, destination, ops); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost after data synchronization", err)
	}
	assertAbsent(t, destination)
}

func TestDirectorySyncFailureRetainsFinalNameIncludingCallerReplacement(t *testing.T) {
	for _, scenario := range []string{"retained-output", "replaced-by-caller"} {
		t.Run(scenario, func(t *testing.T) {
			source, destination := publicationFiles(t)
			ops := systemOperations()
			ops.syncDir = func(string) error {
				if scenario == "replaced-by-caller" {
					if err := os.Remove(destination); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(destination, []byte("caller result"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return syscall.EIO
			}
			ops.remove = func(string) error {
				t.Fatal("post-link rollback could delete another caller's replacement")
				return nil
			}
			_, err := publish(context.Background(), source, destination, ops)
			if !errors.Is(err, syscall.EIO) {
				t.Fatal("underlying synchronization error was lost", err)
			}
			if !errors.Is(err, ErrPublicationUncertain) {
				t.Fatal("uncertain publication reported an ordinary failure", err)
			}
			if scenario == "replaced-by-caller" {
				if data, err := os.ReadFile(destination); err != nil || string(data) != "caller result" {
					t.Fatal("cleanup removed or changed another caller's file", err)
				}
			}
			if scenario == "retained-output" {
				if data, err := os.ReadFile(destination); err != nil || string(data) != "complete result" {
					t.Fatal("uncertain result was not retained", err)
				}
			}
		})
	}
}

func TestPublicationPreservesExistingFileAndDanglingSymlink(t *testing.T) {
	for _, scenario := range []string{"file", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			source, destination := publicationFiles(t)
			if scenario == "file" {
				if err := os.WriteFile(destination, []byte("caller result"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink("absent-target", destination); err != nil {
				t.Skip("symlink creation is not available: " + err.Error())
			}
			before, err := os.Lstat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Publish(context.Background(), source, destination); !errors.Is(err, os.ErrExist) {
				t.Fatal("existing output was not rejected", err)
			}
			after, err := os.Lstat(destination)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("existing output was replaced", err)
			}
			if scenario == "file" {
				if data, err := os.ReadFile(destination); err != nil || string(data) != "caller result" {
					t.Fatal("existing output bytes changed", err)
				}
			}
		})
	}
}

func TestConcurrentPublicationHasExactlyOneCompleteWinner(t *testing.T) {
	source, destination := publicationFiles(t)
	requirePublicationSupport(t, source, destination)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Publish(context.Background(), source, destination); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, os.ErrExist) {
				t.Error("unexpected concurrent publication failure", err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("exclusive publication did not have one winner", successes.Load())
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "complete result" {
		t.Fatal("winner was not a complete result", err)
	}
}

func TestPreflightUnsupportedLinkCleansAndSyncsProbeRemoval(t *testing.T) {
	staging, destination := t.TempDir(), t.TempDir()
	ops := systemOperations()
	ops.link = func(string, string) error { return syscall.ENOTSUP }
	var synchronized []string
	ops.syncDir = func(path string) error {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatal("probe removal was not complete before cleanup synchronization", entries, err)
		}
		synchronized = append(synchronized, path)
		return nil
	}
	if err := preflight(staging, destination, ops); !errors.Is(err, ErrUnsupportedFilesystem) || !errors.Is(err, syscall.ENOTSUP) {
		t.Fatal("unsupported filesystem was not explicit", err)
	}
	if len(synchronized) != 2 || synchronized[0] != staging || synchronized[1] != destination {
		t.Fatal("probe cleanup did not synchronize both affected directories", synchronized)
	}
}

func TestPreflightCleanupFailureDoesNotClaimSupportedPublication(t *testing.T) {
	staging, destination := t.TempDir(), t.TempDir()
	ops := systemOperations()
	ops.syncDir = func(string) error { return nil }
	ops.remove = func(string) error { return syscall.EACCES }
	if err := preflight(staging, destination, ops); !errors.Is(err, ErrSyncFailed) || !errors.Is(err, syscall.EACCES) {
		t.Fatal("preflight ignored failed probe cleanup", err)
	}
}

func TestSyncChecksFileAndAllKnownParentsBeforeReturning(t *testing.T) {
	for _, scenario := range []string{"file", "containing-directory", "known-parent"} {
		t.Run(scenario, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "artifacts")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "result")
			if err := os.WriteFile(path, []byte("result"), 0600); err != nil {
				t.Fatal(err)
			}
			ops := systemOperations()
			ops.syncFile = func(*os.File) error {
				if scenario == "file" {
					return syscall.EIO
				}
				return nil
			}
			ops.syncDir = func(path string) error {
				if scenario == "containing-directory" && path == dir || scenario == "known-parent" && path == parent {
					return syscall.EIO
				}
				return nil
			}
			if _, err := syncPath(path, []string{parent}, ops); !errors.Is(err, ErrSyncFailed) || !errors.Is(err, syscall.EIO) {
				t.Fatal("publication barrier ignored synchronization failure", err)
			}
		})
	}
}
