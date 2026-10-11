//go:build !linux && !darwin && !windows

package app

// Unsupported targets cannot acknowledge unmeasured physical headroom.
func filesystemStorageAvailable(_ string, _ int64) bool { return false }
