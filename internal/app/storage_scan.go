package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

const storageScanBatchSize = 128

// scanStorageDirectory does not sort or retain the complete directory. Storage
// admission only needs a byte sum and can stop at its first limit or deadline.
func scanStorageDirectory(ctx context.Context, path string, visit func(string, os.DirEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(storageScanBatchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(filepath.Join(path, entry.Name()), entry); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
