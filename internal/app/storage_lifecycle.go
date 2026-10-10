package app

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"os"
	"path/filepath"
	"time"
)

type storageDeletionFile struct {
	path, owner string
	size        int64
}

// A retention batch shares one statement and one counter-trigger update.
// Duplicate paths keep the largest observed size, as individual upserts did.
func tombstoneStorageFiles(ctx context.Context, tx pgx.Tx, files []storageDeletionFile) error {
	paths := make([]string, 0, len(files))
	owners := make([]string, 0, len(files))
	sizes := make([]int64, 0, len(files))
	indices := make(map[string]int, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.path == "" {
			continue
		}
		size := observedSize(file.path, file.size)
		if i, ok := indices[file.path]; ok {
			sizes[i] = max(sizes[i], size)
			continue
		}
		indices[file.path] = len(paths)
		paths = append(paths, file.path)
		owners = append(owners, file.owner)
		sizes = append(sizes, size)
	}
	if len(paths) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO storage_files(path,owner,kind,size_bytes,delete_pending,delete_retry_at) SELECT path,owner,'tombstone',size,true,statement_timestamp() FROM unnest($1::text[],$2::text[],$3::bigint[]) AS f(path,owner,size) ON CONFLICT(path) DO UPDATE SET delete_pending=true,size_bytes=GREATEST(storage_files.size_bytes,excluded.size_bytes),delete_retry_at=CASE WHEN storage_files.delete_pending THEN storage_files.delete_retry_at ELSE excluded.delete_retry_at END,delete_failures=CASE WHEN storage_files.delete_pending THEN storage_files.delete_failures ELSE 0 END`, paths, owners, sizes)
	return err
}
func pendingStoragePaths(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT path FROM storage_files WHERE delete_pending AND delete_retry_at<=statement_timestamp() ORDER BY delete_retry_at,path LIMIT $1`, storageBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

type storageCleanupBatch struct {
	paths []string
	full  bool
}

func (s *Store) Cleanup(ctx context.Context, artifactTTL, sourceTTL time.Duration) ([]string, error) {
	batch, err := s.cleanupStorageBatch(ctx, artifactTTL, sourceTTL)
	return batch.paths, err
}

func (s *Store) cleanupStorageBatch(ctx context.Context, artifactTTL, sourceTTL time.Duration) (storageCleanupBatch, error) {
	cacheCtx, cancel := context.WithTimeout(ctx, platformMetadataDBTimeout)
	_, _ = s.DB.Exec(cacheCtx, "DELETE FROM source_metadata_cache WHERE expires_at<=now()")
	cancel()
	tx, err := s.storageTx(ctx)
	if err != nil {
		return storageCleanupBatch{}, err
	}
	defer rollbackStorage(tx)
	// Maintenance must give admission/publication the shared lock back promptly
	// when an operator or migration holds a conflicting table/resource lock.
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='750ms'"); err != nil {
		return storageCleanupBatch{}, err
	}

	// Materialize at most one deletion batch, then match its IDs directly through
	// the primary key instead of scanning the table again in a semi join. OFFSET 0
	// keeps artifact/job pin probes correlated: the small expired candidate set
	// must not require hashing all retained dependency rows. Sources intentionally
	// retain their set-based pin checks; an old, mostly pinned source backlog can
	// make per-source indexed probes substantially slower than one hash anti join.
	// Either expiry partition can contribute at most the whole batch. Taking its
	// oldest eligible batch first preserves the global created_at order while
	// allowing the legacy NULL-expiry index to stop without sorting every row.
	rows, err := tx.Query(ctx, `WITH artifact_retention_candidates AS MATERIALIZED (
 SELECT id FROM (
  (SELECT a.id,a.created_at FROM artifacts a
   WHERE a.expires_at<=now()
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.id=a.job_id AND j.status IN ('queued','running','waiting_storage') OFFSET 0)
   ORDER BY a.created_at LIMIT $2)
  UNION ALL
  (SELECT a.id,a.created_at FROM artifacts a
   WHERE a.expires_at IS NULL AND a.created_at<=now()-($1*interval '1 second')
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.id=a.job_id AND j.status IN ('queued','running','waiting_storage') OFFSET 0)
   ORDER BY a.created_at LIMIT $2)
 ) expired ORDER BY created_at LIMIT $2
)
DELETE FROM artifacts AS a WHERE a.id=ANY(ARRAY(SELECT id FROM artifact_retention_candidates))
RETURNING a.path,a.owner,a.size_bytes`, artifactTTL.Seconds(), storageBatch)
	if err != nil {
		return storageCleanupBatch{}, err
	}
	var files []storageDeletionFile
	for rows.Next() {
		var v storageDeletionFile
		if err = rows.Scan(&v.path, &v.owner, &v.size); err != nil {
			rows.Close()
			return storageCleanupBatch{}, err
		}
		files = append(files, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return storageCleanupBatch{}, err
	}
	full := len(files) == storageBatch
	if err = tombstoneStorageFiles(ctx, tx, files); err != nil {
		return storageCleanupBatch{}, err
	}
	jobs, err := tx.Exec(ctx, `WITH job_retention_candidates AS MATERIALIZED (
 SELECT j.id FROM jobs j
 WHERE j.status NOT IN ('queued','running','waiting_storage')
 AND j.updated_at<now()-($1*interval '1 second')
 AND NOT EXISTS(SELECT 1 FROM artifacts WHERE job_id=j.id OFFSET 0)
 ORDER BY j.updated_at LIMIT $2
)
DELETE FROM jobs AS j WHERE j.id=ANY(ARRAY(SELECT id FROM job_retention_candidates))`, artifactTTL.Seconds(), storageBatch)
	if err != nil {
		return storageCleanupBatch{}, err
	}
	full = full || jobs.RowsAffected() == storageBatch
	rows, err = tx.Query(ctx, `WITH source_retention_candidates AS MATERIALIZED (
 SELECT source.id FROM sources source
 WHERE source.created_at<now()-($1*interval '1 second')
 AND NOT EXISTS(SELECT 1 FROM jobs WHERE source_id=source.id)
 AND NOT EXISTS(SELECT 1 FROM storage_reservations WHERE kind='preview' AND job_id=source.id)
 ORDER BY source.created_at LIMIT $2
)
DELETE FROM sources AS source WHERE source.id=ANY(ARRAY(SELECT id FROM source_retention_candidates))
RETURNING source.path,source.thumbnail_path,source.owner`, sourceTTL.Seconds(), storageBatch)
	if err != nil {
		return storageCleanupBatch{}, err
	}
	files = nil
	sourceCount := 0
	for rows.Next() {
		sourceCount++
		var path, thumb, owner string
		if err = rows.Scan(&path, &thumb, &owner); err != nil {
			rows.Close()
			return storageCleanupBatch{}, err
		}
		for _, p := range []string{path, thumb} {
			if p != "" {
				files = append(files, storageDeletionFile{path: p, owner: owner})
			}
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return storageCleanupBatch{}, err
	}
	if err = tombstoneStorageFiles(ctx, tx, files); err != nil {
		return storageCleanupBatch{}, err
	}
	paths, err := pendingStoragePaths(ctx, tx)
	if err != nil {
		return storageCleanupBatch{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return storageCleanupBatch{}, err
	}
	return storageCleanupBatch{paths: paths, full: full || sourceCount == storageBatch || len(paths) == storageBatch}, nil
}

func (s *Store) DeleteSource(ctx context.Context, id, owner string) ([]string, error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackStorage(tx)
	var path, thumb string
	if err = tx.QueryRow(ctx, "SELECT path,thumbnail_path FROM sources WHERE id=$1 AND owner=$2 FOR UPDATE", id, owner).Scan(&path, &thumb); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var active bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE source_id=$1 AND status IN ('queued','running','waiting_storage')) OR EXISTS(SELECT 1 FROM storage_reservations WHERE kind='preview' AND job_id=$1)", id).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, ErrSourceInUse
	}
	if err = deleteSourcePreviews(ctx, tx, s.storageConfig(), id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `DELETE FROM artifacts WHERE job_id IN (SELECT id FROM jobs WHERE source_id=$1) RETURNING path,size_bytes`, id)
	if err != nil {
		return nil, err
	}
	var files []storageDeletionFile
	for rows.Next() {
		v := storageDeletionFile{owner: owner}
		if err = rows.Scan(&v.path, &v.size); err != nil {
			rows.Close()
			return nil, err
		}
		files = append(files, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	files = append(files, storageDeletionFile{path: path, owner: owner}, storageDeletionFile{path: thumb, owner: owner})
	if err = tombstoneStorageFiles(ctx, tx, files); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM jobs WHERE source_id=$1", id); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM sources WHERE id=$1 AND owner=$2", id, owner); err != nil {
		return nil, err
	}
	paths, err := pendingStoragePaths(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return paths, nil
}

func (s *Store) ReconcileStorage(ctx context.Context, c Config) error {
	return s.reconcileStorageProgress(ctx, c, storageMaintenanceBatches)
}

func (s *Store) reconcileStorageProgress(ctx context.Context, c Config, batches int) error {
	var problems []error
	for attempt := 0; attempt < batches; attempt++ {
		p, err := s.nextStorageScan(ctx, c, reconcileScanKinds, attempt == 0)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return errors.Join(append(problems, err)...)
		}
		if err = s.storageScanStep(ctx, c, p); err != nil {
			problems = append(problems, err)
			if ctx.Err() != nil {
				return errors.Join(problems...)
			}
		}
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='750ms'"); err != nil {
		return errors.Join(append(problems, err)...)
	}
	floor := c.JobTimeout + c.SourceTimeout + time.Hour
	ttl := max(c.SourceTTL, c.ArtifactTTL, floor)
	if _, err = tx.Exec(ctx, `WITH orphan_retention_candidates AS MATERIALIZED (
 SELECT path FROM storage_files
 WHERE kind='orphan' AND NOT delete_pending AND observed_at<statement_timestamp()-($1*interval '1 second')
 ORDER BY observed_at LIMIT $2
)
UPDATE storage_files AS file SET delete_pending=true,delete_retry_at=statement_timestamp(),delete_failures=0
FROM orphan_retention_candidates AS candidate WHERE file.path=candidate.path`, ttl.Seconds(), storageBatch); err != nil {
		return errors.Join(append(problems, err)...)
	}
	// A completed sources generation proves only preparations already expired
	// when that pass began. Partial coverage and expirations during a pass retain
	// their full reservation until a later complete, committed generation.
	if _, err = tx.Exec(ctx, `DELETE FROM storage_reservations WHERE id IN (SELECT id FROM storage_reservations WHERE kind='source' AND expires_at<=(SELECT started_at FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind='reconcile_sources' AND path=$2 AND completed_at IS NOT NULL) ORDER BY expires_at,id LIMIT $3)`, c.DataDir, filepath.Join(c.DataDir, "sources"), storageBatch); err != nil {
		return errors.Join(append(problems, err)...)
	}
	_, err = tx.Exec(ctx, `DELETE FROM queue_owners WHERE owner IN (SELECT q.owner FROM queue_owners q WHERE NOT EXISTS(SELECT 1 FROM jobs j WHERE j.owner=q.owner) LIMIT $1)`, storageBatch)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return errors.Join(append(problems, err)...)
}

// Remove one temporary tree with fixed directory batches and cancellation
// between entries. Symlinks are unlinked and never traversed.
func removeStorageTree(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	err = scanStorageDirectory(ctx, path, func(child string, entry os.DirEntry) error { return removeStorageTree(ctx, child) })
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) reconcileDirectory(ctx context.Context, path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(storageScanBatchSize)
		paths := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				paths = append(paths, filepath.Join(path, entry.Name()))
			}
		}
		if len(paths) > 0 {
			rows, e := s.DB.Query(ctx, "SELECT path,kind,size_bytes,observed_at FROM storage_files WHERE path=ANY($1)", paths)
			if e != nil {
				return e
			}
			type observation struct {
				kind     string
				size     int64
				modified time.Time
			}
			known := make(map[string]observation, len(paths))
			for rows.Next() {
				var p string
				var seen observation
				if e = rows.Scan(&p, &seen.kind, &seen.size, &seen.modified); e != nil {
					rows.Close()
					return e
				}
				known[p] = seen
			}
			rows.Close()
			if e = rows.Err(); e != nil {
				return e
			}
			var unknownPaths []string
			var sizes []int64
			var modified []time.Time
			for _, entry := range entries {
				p := filepath.Join(path, entry.Name())
				seen := known[p]
				if !entry.Type().IsRegular() || (seen.kind != "" && seen.kind != "orphan") {
					continue
				}
				info, e := entry.Info()
				if os.IsNotExist(e) {
					continue
				}
				if e != nil {
					return e
				}
				// PostgreSQL timestamps store microseconds. Unchanged orphan
				// files need no write transaction, tuple rewrite or counter trigger.
				mtime := info.ModTime().Truncate(time.Microsecond)
				if seen.kind == "orphan" && info.Size() <= seen.size && !mtime.After(seen.modified) {
					continue
				}
				unknownPaths = append(unknownPaths, p)
				sizes = append(sizes, info.Size())
				modified = append(modified, mtime)
			}
			if len(unknownPaths) > 0 {
				tx, e := s.storageTx(ctx)
				if e != nil {
					return e
				}
				_, e = tx.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,observed_at) SELECT path,'orphan',size,modified FROM unnest($1::text[],$2::bigint[],$3::timestamptz[]) AS f(path,size,modified) ON CONFLICT(path) DO UPDATE SET size_bytes=GREATEST(storage_files.size_bytes,excluded.size_bytes),observed_at=GREATEST(storage_files.observed_at,excluded.observed_at) WHERE storage_files.kind='orphan' AND NOT storage_files.delete_pending AND (storage_files.size_bytes<excluded.size_bytes OR storage_files.observed_at<excluded.observed_at)`, unknownPaths, sizes, modified)
				if e == nil {
					e = tx.Commit(ctx)
				}
				rollbackStorage(tx)
				if e != nil {
					return e
				}
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
