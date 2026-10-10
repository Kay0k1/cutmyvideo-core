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
				if fail {
					return errors.Join(ErrSyncFailed, syscall.EIO)
				}
				return SyncDirectories(directories...)
			})
			if fail {
				if !errors.Is(err, ErrSyncFailed) || !errors.Is(err, syscall.EIO) || barriers != 1 {
					t.Fatal("failed hierarchy synchronization claimed completion", barriers, err)
				}
				if _, err := os.Stat(filepath.Join(first, "nested")); !os.IsNotExist(err) {
					t.Fatal("failed parent barrier permitted deeper publication directories", err)
				}
			} else {
				if err != nil || barriers != 3 {
					t.Fatal("new hierarchy did not synchronize each new parent entry", barriers, err)
				}
				if info, err := os.Stat(final); err != nil || !info.IsDir() {
					t.Fatal("complete publication hierarchy is unavailable", err)
				}
			}
		})
	}
}
