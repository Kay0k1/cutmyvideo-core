//go:build linux

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

const draftCacheHintSupported = true

// Best effort on the completed draft inode only. Concurrent/repeated readers
// may refault cached pages; errors never change an already-written response.
func discardCompletedDraftCache(file *os.File) {
	connection, err := file.SyscallConn()
	if err != nil {
		return
	}
	_ = connection.Control(func(fd uintptr) {
		_ = unix.Fadvise(int(fd), 0, 0, unix.FADV_DONTNEED)
	})
}
