package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageScanCrossesBatchesAndStopsImmediately(t *testing.T) {
	dir := t.TempDir()
	for i := range storageScanBatchSize*2 + 3 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	if err := scanStorageDirectory(context.Background(), dir, func(path string, entry os.DirEntry) error {
		if seen[path] || !entry.Type().IsRegular() {
			t.Fatalf("duplicate or unexpected entry %s", path)
		}
		seen[path] = true
		return nil
	}); err != nil || len(seen) != storageScanBatchSize*2+3 {
		t.Fatalf("incomplete batched scan: %d entries, %v", len(seen), err)
	}
	stopped := errors.New("quota reached")
	visits := 0
	if err := scanStorageDirectory(context.Background(), dir, func(string, os.DirEntry) error {
		visits++
		return stopped
	}); !errors.Is(err, stopped) || visits != 1 {
		t.Fatalf("continued scanning after quota: %d visits, %v", visits, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	visits = 0
	if err := scanStorageDirectory(ctx, dir, func(string, os.DirEntry) error {
		visits++
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) || visits != 1 {
		t.Fatalf("continued scanning after cancellation: %d visits, %v", visits, err)
	}
}

func TestStorageAdmissionCountsNestedFilesWithoutFollowingLinks(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "work", "job", "fragment")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "media"), make([]byte, 64), 0600); err != nil {
		t.Fatal(err)
	}
	// A symlink back to the storage root must neither double count nor recurse.
	if err := os.Symlink(dir, filepath.Join(nested, "loop")); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	c := Config{DataDir: dir, MaxStorageBytes: 128}
	if !storageAvailable(c, 64) || storageAvailable(c, 65) {
		t.Fatal("nested regular files were omitted or symbolic links were followed")
	}
}
