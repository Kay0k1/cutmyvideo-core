//go:build windows

package fsdurable

import (
	"errors"
	"syscall"
)

func unsupportedOperation(err error) bool {
	// Native Windows errors: NOT_SUPPORTED (50), INVALID_HANDLE (6),
	// INVALID_FUNCTION (1), and NOT_SAME_DEVICE (17). ACCESS_DENIED is a
	// permission failure, not evidence that the filesystem lacks a capability.
	return errors.Is(err, syscall.Errno(50)) || errors.Is(err, syscall.Errno(6)) ||
		errors.Is(err, syscall.Errno(1)) || errors.Is(err, syscall.Errno(17)) ||
		errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EXDEV)
}
