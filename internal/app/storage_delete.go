package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

// A retry error means all database work committed: only individual filesystem
// paths failed. Maintenance can continue its bounded batches; caller context,
// SQL and uncertain COMMIT errors instead stop the cycle.
type storageDeletionRetryError struct{ causes []error }

func (e *storageDeletionRetryError) Error() string {
	return fmt.Sprintf("%d persistent media deletions scheduled for retry: %v", len(e.causes), e.causes[0])
}
func (e *storageDeletionRetryError) Unwrap() []error { return e.causes }

func storageDeletionRetryOnly(err error) bool {
	_, retry := err.(*storageDeletionRetryError)
	return retry
}

type storageDeleteOperations struct {
	lstat       func(string) (fs.FileInfo, error)
	remove      func(string) error
	synchronize func(...string) error
	commit      func(context.Context, pgx.Tx) error
}

type storageDeleteCandidate struct {
	path       string
	referenced bool
}

func systemStorageDeleteOperations() storageDeleteOperations {
	return storageDeleteOperations{
		lstat: os.Lstat, remove: os.Remove, synchronize: fsdurable.SyncDirectories,
		commit: func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) },
	}
}

// DrainStorageDeletes revalidates due tombstones under the same fence used by
// registration and metadata deletion. Completed removal and directory sync
// precede releasing its charge; failures persist bounded exponential retries.
// Stale supplied lists cannot bypass backoff or remove a newly registered file.
func (s *Store) DrainStorageDeletes(ctx context.Context, c Config, paths []string) error {
	return s.drainStorageDeletes(ctx, c, paths, systemStorageDeleteOperations())
}

func (s *Store) drainStorageDeletes(ctx context.Context, c Config, paths []string, ops storageDeleteOperations) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var retries []error
	for len(paths) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := paths[:min(len(paths), storageBatch)]
		paths = paths[len(batch):]
		err := s.drainStorageDeleteBatch(ctx, c, batch, ops)
		if retry, ok := err.(*storageDeletionRetryError); ok {
			retries = append(retries, retry.causes...)
		} else if err != nil {
			return err
		}
	}
	if len(retries) > 0 {
		return &storageDeletionRetryError{causes: retries}
	}
	return nil
}

func (s *Store) drainStorageDeleteBatch(ctx context.Context, c Config, paths []string, ops storageDeleteOperations) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='750ms'"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT f.path,EXISTS(SELECT 1 FROM sources s WHERE (s.path=f.path AND s.path<>'') OR (s.thumbnail_path=f.path AND s.thumbnail_path<>'')) OR EXISTS(SELECT 1 FROM artifacts a WHERE a.path=f.path AND a.path<>'') FROM storage_files f WHERE f.path=ANY($1) AND f.delete_pending AND f.delete_retry_at<=statement_timestamp() ORDER BY f.delete_retry_at,f.path LIMIT $2 FOR UPDATE OF f`, paths, storageBatch)
	if err != nil {
		return err
	}
	selected, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storageDeleteCandidate, error) {
		var candidate storageDeleteCandidate
		err := row.Scan(&candidate.path, &candidate.referenced)
		return candidate, err
	})
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return nil
	}
	var failed []string
	var causes []error
	removedByParent := make(map[string][]string)
	for _, candidate := range selected {
		path := candidate.path
		if err = ctx.Err(); err != nil {
			return err
		}
		var parent string
		var fileErr error
		if candidate.referenced {
			fileErr = errors.New("persistent media path still has a live database reference")
		} else {
			parent, fileErr = storageDeletionParent(c, path)
		}
		if fileErr == nil {
			info, inspectErr := ops.lstat(path)
			fileErr = inspectErr
			if inspectErr == nil {
				if !info.Mode().IsRegular() {
					fileErr = errors.New("refusing non-regular persistent media path")
				} else {
					fileErr = ops.remove(path)
				}
			}
			if errors.Is(fileErr, os.ErrNotExist) {
				fileErr = nil
			}
		}
		if fileErr != nil {
			if errors.Is(fileErr, context.Canceled) || errors.Is(fileErr, context.DeadlineExceeded) {
				return fileErr
			}
			failed = append(failed, path)
			causes = append(causes, fileErr)
			continue
		}
		removedByParent[parent] = append(removedByParent[parent], path)
	}
	var acknowledged []string
	for parent, removed := range removedByParent {
		if err = ctx.Err(); err != nil {
			return err
		}
		if syncErr := ops.synchronize(parent); syncErr != nil {
			if errors.Is(syncErr, context.Canceled) || errors.Is(syncErr, context.DeadlineExceeded) {
				return syncErr
			}
			failed = append(failed, removed...)
			for range removed {
				causes = append(causes, syncErr)
			}
		} else {
			acknowledged = append(acknowledged, removed...)
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if len(failed) > 0 {
		// First retry is 30 seconds; repeated failures double up to one hour.
		// The due-time index moves poison entries behind untouched tombstones.
		_, err = tx.Exec(ctx, `UPDATE storage_files SET delete_retry_at=clock_timestamp()+LEAST(3600,30*power(2,LEAST(delete_failures,7)))*interval '1 second',delete_failures=LEAST(delete_failures::bigint+1,2147483647)::integer WHERE path=ANY($1) AND delete_pending`, failed)
		if err != nil {
			return err
		}
	}
	if len(acknowledged) > 0 {
		if _, err = tx.Exec(ctx, "DELETE FROM storage_files WHERE path=ANY($1) AND delete_pending", acknowledged); err != nil {
			return err
		}
	}
	if err = ops.commit(ctx, tx); err != nil {
		return err
	}
	if len(causes) > 0 {
		return &storageDeletionRetryError{causes: causes}
	}
	return nil
}

// The configured root may itself be a symlink, but a child directory must not
// escape its resolved root. For an already missing hierarchy, synchronizing
// its closest existing parent persists the removal of the missing child entry.
func storageDeletionParent(c Config, path string) (string, error) {
	if !storagePathInside(c.DataDir, path) {
		return "", errors.New("refusing storage path outside data directory")
	}
	base, err := filepath.Abs(c.DataDir)
	if err != nil {
		return "", err
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for parent := filepath.Dir(target); ; parent = filepath.Dir(parent) {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			info, err := os.Stat(resolved)
			if err != nil {
				return "", err
			}
			if !info.IsDir() {
				return "", errors.New("persistent media parent is not a directory")
			}
			relative, relationErr := filepath.Rel(realBase, resolved)
			if relationErr != nil || !filepath.IsLocal(relative) {
				return "", errors.New("refusing persistent media parent outside data directory")
			}
			return parent, nil
		}
		if !errors.Is(err, os.ErrNotExist) || parent == base {
			return "", err
		}
	}
}
