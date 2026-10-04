//go:build linux

package app

import (
	"math"
	"os"
	"path/filepath"
	"syscall"
)

func filesystemAvailableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	for {
		err := syscall.Statfs(path, &stat)
		if err == nil {
			if stat.Bsize <= 0 || stat.Bavail > math.MaxUint64/uint64(stat.Bsize) {
				return 0, errStorageBudgetExceeded
			}
			return stat.Bavail * uint64(stat.Bsize), nil
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, err
		}
		// Admission can precede creation of a new data directory.
		path = parent
	}
}

func filesystemStorageAvailable(path string, reserve int64) bool {
	free, err := filesystemAvailableBytes(path)
	return err == nil && reserve >= 0 && uint64(reserve) <= free
}
