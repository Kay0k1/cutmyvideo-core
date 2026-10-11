//go:build windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func filesystemAvailableBytes(path string) (uint64, error) {
	for {
		info, err := os.Stat(path)
		if err == nil {
			// GetDiskFreeSpaceExW requires a directory. In particular, do not
			// climb past a regular file in an invalid data-directory path.
			if !info.IsDir() {
				return 0, errStorageBudgetExceeded
			}
			var free uint64
			free, err = windowsAvailableBytes(path)
			if err == nil {
				return free, nil
			}
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

func windowsAvailableBytes(path string) (uint64, error) {
	if err := getDiskFreeSpaceEx.Find(); err != nil {
		return 0, err
	}
	// A UNC share requires a trailing backslash; it is valid for local paths
	// too. UTF-16 preserves native directory names without a lossy ANSI call.
	if !strings.HasSuffix(path, string(os.PathSeparator)) {
		path += string(os.PathSeparator)
	}
	directory, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	ok, _, err := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(directory)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if ok == 0 {
		if err == nil || err == syscall.Errno(0) {
			return 0, errStorageBudgetExceeded
		}
		return 0, err
	}
	// The first output honors the caller's quota; total volume free space can
	// overstate admission capacity. Keep the complete 64-bit byte count.
	return free, nil
}

func filesystemStorageAvailable(path string, reserve int64) bool {
	free, err := filesystemAvailableBytes(path)
	return err == nil && reserve >= 0 && uint64(reserve) <= free
}
