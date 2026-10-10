//go:build windows

package fsdurable

import (
	"errors"
	"syscall"
)

func unsupportedOperation(err error) bool {
	// Native Windows errors: NOT_SUPPORTED (50), INVALID_HANDLE (6),
	// INVALID_FUNCTION (1), and NOT_SAME_DEVICE (17). ACCESS_DENIED also describes Windows' refusal to
	// flush an otherwise readable directory handle.
	return errors.Is(err, syscall.Errno(50)) || errors.Is(err, syscall.Errno(6)) ||
		errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(1)) || errors.Is(err, syscall.Errno(17)) ||
		errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EXDEV)
}
