//go:build linux

package app

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageAdmissionChecksFilesystemSpaceIndependentlyOfLogicalCap(t *testing.T) {
	dir := t.TempDir()
	free, err := filesystemAvailableBytes(dir)
	if err != nil || free >= math.MaxInt64 {
		t.Fatal("could not measure the test filesystem", err)
	}
	c := Config{DataDir: dir, MaxStorageBytes: math.MaxInt64}
	if storageAvailable(c, int64(free)+1) {
		t.Fatal("logical headroom permitted a reservation larger than filesystem free space")
	}
	if !storageAvailable(c, 1) {
		t.Fatal("small reservation on empty available storage was refused")
	}
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c.DataDir = filepath.Join(file, "data")
	if storageAvailable(c, 1) {
		t.Fatal("unreadable storage location must fail closed")
	}
}
