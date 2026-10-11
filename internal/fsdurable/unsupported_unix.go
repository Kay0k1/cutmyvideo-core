//go:build !windows

package fsdurable

import (
	"errors"
	"syscall"
)

func unsupportedOperation(err error) bool {
	return errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EPERM)
}
