package fsdurable

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnsureDirectory creates the missing portion of an application-owned directory
// hierarchy and persists each new directory and its entry in the known parent.
// Existing ancestors are assumed to have been persisted by their owner.
func EnsureDirectory(path string, mode fs.FileMode) error {
	return ensureDirectory(path, mode, SyncDirectories)
}

func ensureDirectory(path string, mode fs.FileMode, synchronize func(...string) error) error {
	path = filepath.Clean(path)
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%w: publication parent is not a directory", ErrPublicationFailed)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect publication parent: %w", ErrPublicationFailed, err)
		}
		missing = append(missing, current)
		if filepath.Dir(current) == current {
			return fmt.Errorf("%w: publication hierarchy has no existing ancestor", ErrPublicationFailed)
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		dir := missing[i]
		if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: create publication directory: %w", ErrPublicationFailed, err)
		}
		if err := synchronize(dir, filepath.Dir(dir)); err != nil {
			return err
		}
	}
	return nil
}
