//go:build !windows

package fsdurable

import (
	"errors"
	"os"
)

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	info, err := dir.Stat()
	if err == nil && !info.IsDir() {
		err = errors.New("publication parent is not a directory")
	}
	if err == nil {
		err = dir.Sync()
	}
	return errors.Join(err, dir.Close())
}
