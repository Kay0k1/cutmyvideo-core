//go:build windows

package fsdurable

import (
	"errors"
	"os"
	"syscall"
)

func syncDirectory(path string) error {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// FlushFileBuffers requires GENERIC_WRITE. Go's ordinary read-only
	// directory handle cannot flush, and os.OpenFile deliberately rejects
	// writable directories. BACKUP_SEMANTICS permits this native handle.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return &os.PathError{Op: "open directory for synchronization", Path: path, Err: err}
	}
	dir := os.NewFile(uintptr(handle), path)
	info, err := dir.Stat()
	if err == nil && !info.IsDir() {
		err = errors.New("publication parent is not a directory")
	}
	if err == nil {
		err = dir.Sync()
	}
	return errors.Join(err, dir.Close())
}
