package fsdurable

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureDirectoryPersistsNewHierarchyAndStopsAtFailedBarrier(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "complete"
		if fail {
			name = "failed-parent-barrier"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			first := filepath.Join(root, "new")
			final := filepath.Join(first, "nested", "media")
			barriers := 0
			err := ensureDirectory(final, 0700, func(directories ...string) error {
				barriers++
				// Every new entry is synchronized in its immediate existing
				// parent before a deeper directory or output can be created.
				if len(directories) != 2 || filepath.Dir(directories[0]) != directories[1] {
					t.Fatal("directory barrier did not include its immediate parent", directories)
				}
				if fail && directories[0] == first {
					return errors.Join(ErrSyncFailed, syscall.EIO)
				}
				return SyncDirectories(directories...)
			})
			if fail {
				if !errors.Is(err, ErrSyncFailed) || !errors.Is(err, syscall.EIO) || barriers != 2 {
					t.Fatal("failed hierarchy synchronization claimed completion", barriers, err)
				}
				if _, err := os.Stat(filepath.Join(first, "nested")); !os.IsNotExist(err) {
					t.Fatal("failed parent barrier permitted deeper publication directories", err)
				}
			} else {
				if err != nil || barriers != 4 {
					t.Fatal("new hierarchy did not synchronize each new parent entry", barriers, err)
				}
				if info, err := os.Stat(final); err != nil || !info.IsDir() {
					t.Fatal("complete publication hierarchy is unavailable", err)
				}
			}
		})
	}
}

func TestEnsureDirectoryRetryPersistsInterruptedEntryBeforeCreatingChildren(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "new")
	child := filepath.Join(first, "nested")
	final := filepath.Join(child, "media")
	ops := systemOperations()
	failed := false
	ops.syncDir = func(path string) error {
		if path == root {
			if _, err := os.Stat(first); err == nil {
				failed = true
				return syscall.EIO
			}
		}
		return syncDirectory(path)
	}
	err := ensureDirectory(final, 0700, func(directories ...string) error {
		return syncDirectories(directories, ops)
	})
	if !failed || !errors.Is(err, ErrSyncFailed) || !errors.Is(err, syscall.EIO) {
		t.Fatal("new directory's actual parent synchronization did not reject creation", err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal("fixture did not retain the interrupted directory entry", err)
	}
	assertAbsent(t, child)

	var repaired []string
	ops.syncDir = func(path string) error {
		if len(repaired) < 2 {
			assertAbsent(t, child)
			repaired = append(repaired, path)
		}
		return syncDirectory(path)
	}
	if err := ensureDirectory(final, 0700, func(directories ...string) error {
		return syncDirectories(directories, ops)
	}); err != nil {
		t.Fatal("interrupted directory creation could not be retried", err)
	}
	if len(repaired) != 2 || repaired[0] != first || repaired[1] != root {
		t.Fatal("retry created children before persisting the interrupted parent entry", repaired)
	}
	if info, err := os.Stat(final); err != nil || !info.IsDir() {
		t.Fatal("retry did not complete the directory hierarchy", err)
	}

	// An already complete target may itself be the entry left by a failed
	// creation, so a repeat call must persist it and its immediate parent too.
	var existing []string
	if err := ensureDirectory(final, 0700, func(directories ...string) error {
		existing = append(existing, directories...)
		return SyncDirectories(directories...)
	}); err != nil || len(existing) != 2 || existing[0] != final || existing[1] != child {
		t.Fatal("existing target bypassed its recovery barrier", existing, err)
	}
}
