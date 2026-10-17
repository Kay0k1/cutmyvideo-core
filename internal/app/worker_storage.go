package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func storageAvailable(c Config, reserve int64) bool {
	return storageAvailableContext(context.Background(), c, reserve)
}

func storageAvailableContext(ctx context.Context, c Config, reserve int64) bool {
	if ctx.Err() != nil || reserve < 0 || reserve > c.MaxStorageBytes || !filesystemStorageAvailable(c.DataDir, reserve) {
		return false
	}
	remaining := c.MaxStorageBytes - reserve
	var visit func(string, os.DirEntry) error
	visit = func(path string, entry os.DirEntry) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			err := scanStorageDirectory(ctx, path, visit)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.Type().IsRegular() {
			info, e := entry.Info()
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if info.Size() < 0 || info.Size() > remaining {
				return errStorageBudgetExceeded
			}
			remaining -= info.Size()
		}
		return nil
	}
	// Lstat preserves WalkDir's rule that symbolic links, including a linked
	// root, are not traversed. Files removed by concurrent cleanup are harmless.
	info, err := os.Lstat(c.DataDir)
	if err == nil {
		err = visit(c.DataDir, fs.FileInfoToDirEntry(info))
	}
	return ctx.Err() == nil && (err == nil || os.IsNotExist(err))
}

func waitForJobStorage(parent context.Context, c Config, s *Store, j *Job, token, work string) bool {
	if parent.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), workerDatabaseTimeout)
	defer cancel()
	if err := removeStorageTree(ctx, work); err != nil {
		return false
	}
	if err := s.WaitForStorage(ctx, j.ID, token, c); err != nil {
		return false
	}
	j.Status, j.Stage = "waiting_storage", "waiting_storage"
	j.Message = "Waiting for free storage"
	return true
}

func storagePressureError(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || exportProblem(err).code == "storage_full"
}
