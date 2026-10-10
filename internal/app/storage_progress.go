package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Accounting must finish before an admission can rely on the storage counter.
// Batches already observed remain committed when this retryable error is returned.
var ErrStorageInitializing = errors.New("storage accounting is initializing")

const storageProgressBatches = 24
const storageMaintenanceBatches = 512
const storageRegistrationBatch = 1024

type storageScanProgress struct {
	kind, path, digest string
	position           int64
	mtime, size        *int64
	started            time.Time
}

var bootstrapScanKinds = []string{"bootstrap_sources", "bootstrap_artifacts", "bootstrap_work"}
var reconcileScanKinds = []string{"reconcile_sources", "reconcile_artifacts"}

func scanRoot(c Config, kind string) string {
	return filepath.Join(c.DataDir, strings.TrimPrefix(strings.TrimPrefix(kind, "bootstrap_"), "reconcile_"))
}

func ensureScanRoots(ctx context.Context, tx pgx.Tx, c Config, kinds []string) error {
	for _, kind := range kinds {
		path := scanRoot(c, kind)
		if kind == "cleanup_work" {
			path = filepath.Join(c.DataDir, "work")
		}
		// A separate scheduling row keeps class fairness independent from the
		// directory's last_visited_at/completed_at verification markers.
		if _, err := tx.Exec(ctx, `INSERT INTO storage_scan_progress(data_dir,scan_kind,path) VALUES($1,$2,$3),($1,'schedule_'||$2,$3) ON CONFLICT DO NOTHING`, c.DataDir, kind, path); err != nil {
			return err
		}
	}
	return nil
}

// Each attempt first commits its scheduling position. A bad directory cannot
// monopolize future cycles, even if its I/O times out or the process restarts.
func (s *Store) nextStorageScan(ctx context.Context, c Config, kinds []string, repeat bool) (storageScanProgress, error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return storageScanProgress{}, err
	}
	defer rollbackStorage(tx)
	if err = ensureScanRoots(ctx, tx, c, kinds); err != nil {
		return storageScanProgress{}, err
	}
	if repeat {
		// Start another pass only after every root of this group has completed.
		// Completed coverage remains available for reservation release until then.
		_, err = tx.Exec(ctx, `UPDATE storage_scan_progress SET position=0,prefix_digest='',generation_mtime_ns=NULL,generation_size=NULL,started_at=clock_timestamp(),completed_at=NULL WHERE data_dir=$1 AND scan_kind=ANY($2) AND NOT EXISTS(SELECT 1 FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=ANY($2) AND completed_at IS NULL)`, c.DataDir, kinds)
		if err != nil {
			return storageScanProgress{}, err
		}
	}
	roots := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		path := scanRoot(c, kind)
		if kind == "cleanup_work" {
			path = filepath.Join(c.DataDir, "work")
		}
		roots = append(roots, path)
	}
	var kind, classPath string
	// Pick among at most three durable class clocks, using an indexed EXISTS
	// for pending directories. New work descendants start at -infinity within
	// their class; they must not displace an unfinished source/artifact class.
	err = tx.QueryRow(ctx, `SELECT root.kind,root.path FROM unnest($2::text[],$3::text[]) AS root(kind,path)
	 JOIN storage_scan_progress AS class ON class.data_dir=$1 AND class.scan_kind='schedule_'||root.kind AND class.path=root.path
	 WHERE EXISTS(SELECT 1 FROM storage_scan_progress AS pending WHERE pending.data_dir=$1 AND pending.scan_kind=root.kind AND pending.completed_at IS NULL)
	 ORDER BY class.last_visited_at,root.kind LIMIT 1 FOR UPDATE OF class`, c.DataDir, kinds, roots).Scan(&kind, &classPath)
	if err != nil {
		return storageScanProgress{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE storage_scan_progress SET last_visited_at=clock_timestamp() WHERE data_dir=$1 AND scan_kind='schedule_'||$2 AND path=$3`, c.DataDir, kind, classPath); err != nil {
		return storageScanProgress{}, err
	}
	var p storageScanProgress
	err = tx.QueryRow(ctx, `SELECT scan_kind,path,position,prefix_digest,generation_mtime_ns,generation_size,started_at FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND completed_at IS NULL ORDER BY last_visited_at,path LIMIT 1 FOR UPDATE`, c.DataDir, kind).Scan(&p.kind, &p.path, &p.position, &p.digest, &p.mtime, &p.size, &p.started)
	if err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, `UPDATE storage_scan_progress SET last_visited_at=clock_timestamp() WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

// One-step callers still close their handles immediately. Public multi-step
// operations use one local session to avoid replaying a committed prefix for
// every batch; no descriptor survives the enclosing operation.
func readStorageScanBatch(ctx context.Context, c Config, p storageScanProgress) ([]os.DirEntry, string, os.FileInfo, bool, bool, error) {
	session := newStorageScanSession()
	defer session.close()
	return session.read(ctx, c, p)
}

type storagePrefix struct {
	digest [sha256.Size]byte
	count  int64
}

func (p *storagePrefix) add(entry os.DirEntry) {
	// Native filenames are at most 255 bytes on Unix or 255 UTF-16 units on
	// Windows. Reuse a stack buffer; prefix replay does not allocate a hasher,
	// hexadecimal string and scratch slices for every previously visited name.
	var scratch [1024 + sha256.Size + 16]byte
	n := 0
	if p.count > 0 {
		n = copy(scratch[:], p.digest[:])
	}
	binary.LittleEndian.PutUint64(scratch[n:n+8], uint64(len(entry.Name())))
	n += 8
	if len(entry.Name()) > 1024 {
		// Keep the helper correct for non-native implementations of DirEntry.
		large := make([]byte, n+len(entry.Name())+8)
		copy(large, scratch[:n])
		n += copy(large[n:], entry.Name())
		binary.LittleEndian.PutUint64(large[n:], uint64(entry.Type()))
		p.digest = sha256.Sum256(large)
	} else {
		n += copy(scratch[n:], entry.Name())
		binary.LittleEndian.PutUint64(scratch[n:n+8], uint64(entry.Type()))
		p.digest = sha256.Sum256(scratch[:n+8])
	}
	p.count++
}

func (p *storagePrefix) encoded() string {
	if p.count == 0 {
		return ""
	}
	return hex.EncodeToString(p.digest[:])
}

// Paths are accepted only as descendants of this configured root. Every parent
// is checked with Lstat: neither a restored SQL path nor a symlink may redirect
// a recursive traversal outside the private media directory.
func safeScanPath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return errors.New("storage scan path is outside its root")
	}
	current := root
	components := []string{}
	if rel != "." {
		components = strings.Split(rel, string(os.PathSeparator))
	}
	for i := -1; i < len(components); i++ {
		if i >= 0 {
			current = filepath.Join(current, components[i])
		}
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("storage scan parent is not a directory")
		}
	}
	return nil
}

func (s *Store) storageScanStep(ctx context.Context, c Config, p storageScanProgress) error {
	session := newStorageScanSession()
	defer session.close()
	return s.storageScanStepSession(ctx, c, p, session)
}

func (s *Store) storageScanStepSession(ctx context.Context, c Config, p storageScanProgress, session *storageScanSession) error {
	retained := false
	defer func() {
		if !retained {
			session.discard(p.kind)
		}
	}()
	entries, digest, info, eof, reset, err := session.read(ctx, c, p)
	if err != nil {
		return err
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	var position int64
	var previous string
	var completed *time.Time
	var started time.Time
	if err = tx.QueryRow(ctx, `SELECT position,prefix_digest,completed_at,started_at FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND path=$3 FOR UPDATE`, c.DataDir, p.kind, p.path).Scan(&position, &previous, &completed, &started); err != nil {
		return err
	}
	if position != p.position || previous != p.digest || completed != nil || !started.Equal(p.started) {
		return nil // Another process committed this batch first.
	}
	if reset {
		position = 0
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&started); err != nil {
			return err
		}
	}
	if p.kind == "cleanup_work" {
		for _, entry := range entries {
			if _, err = tx.Exec(ctx, `INSERT INTO storage_work_cleanup(data_dir,path) VALUES($1,$2) ON CONFLICT DO NOTHING`, c.DataDir, filepath.Join(p.path, entry.Name())); err != nil {
				return err
			}
		}
	} else if err = observeStorageBatch(ctx, tx, c, p, entries); err != nil {
		return err
	}
	// Detect membership changes during replay/stat/SQL. Observations remain
	// conservatively charged, but the changed generation cannot prove coverage.
	var mtime, size *int64
	stable := true
	if info != nil {
		m, n := info.ModTime().UnixNano(), info.Size()
		mtime, size = &m, &n
		after, e := os.Lstat(p.path)
		if e != nil || !os.SameFile(info, after) || after.ModTime() != info.ModTime() || after.Size() != info.Size() {
			stable = false
			position, digest, eof, reset = 0, "", false, true
			mtime, size = nil, nil
			entries = nil
		}
	}
	_, err = tx.Exec(ctx, `UPDATE storage_scan_progress SET position=$4,prefix_digest=$5,generation_mtime_ns=$6,generation_size=$7,started_at=$8,completed_at=CASE WHEN $9 THEN clock_timestamp() ELSE NULL END WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path, position+int64(len(entries)), digest, mtime, size, started, eof)
	if err != nil {
		return err
	}
	if err = session.commit(ctx, tx); err != nil {
		return err
	}
	// A read advances the live handle before SQL. Reuse is permitted only after
	// the exact cursor was acknowledged by COMMIT; unknown outcomes, CAS misses
	// and cancellation force a fresh replay from the persisted cursor.
	if stable && !eof && ctx.Err() == nil {
		retained = session.accept(storageScanProgress{
			kind: p.kind, path: p.path, position: position + int64(len(entries)),
			digest: digest, mtime: mtime, size: size, started: started,
		})
	}
	return nil
}

func observeStorageBatch(ctx context.Context, tx pgx.Tx, c Config, p storageScanProgress, entries []os.DirEntry) error {
	lookup := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			lookup = append(lookup, filepath.Join(p.path, entry.Name()))
		}
	}
	rows, err := tx.Query(ctx, `SELECT path FROM storage_files WHERE path=ANY($1) AND kind NOT IN ('orphan','work_orphan')`, lookup)
	if err != nil {
		return err
	}
	registered := make(map[string]bool, len(lookup))
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		registered[path] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var paths []string
	var sizes []int64
	var modified []time.Time
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(p.path, entry.Name())
		if entry.IsDir() && p.kind == "bootstrap_work" {
			var reserved bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE kind='job' AND job_id||'-'||token=$1)`, entry.Name()).Scan(&reserved); err != nil {
				return err
			}
			if !reserved {
				if _, err := tx.Exec(ctx, `INSERT INTO storage_scan_progress(data_dir,scan_kind,path) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.DataDir, p.kind, path); err != nil {
					return err
				}
			}
		}
		if !entry.Type().IsRegular() || registered[path] {
			continue
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		paths = append(paths, path)
		sizes = append(sizes, info.Size())
		modified = append(modified, info.ModTime().Truncate(time.Microsecond))
	}
	if len(paths) == 0 {
		return nil
	}
	kind := "orphan"
	if p.kind == "bootstrap_work" {
		kind = "work_orphan"
	}
	// Ownership is adopted inside each batch, without a final full-table path
	// rewrite. Published files win; orphan observations only increase charges.
	_, err = tx.Exec(ctx, `INSERT INTO storage_files(path,owner,resource_id,kind,size_bytes,observed_at)
	 SELECT f.path,COALESCE(s.owner,a.owner,''),COALESCE(s.id,a.id,''),CASE WHEN s.id IS NOT NULL THEN 'source' WHEN a.id IS NOT NULL THEN 'artifact' ELSE $4 END,f.size,f.modified
	 FROM unnest($1::text[],$2::bigint[],$3::timestamptz[]) AS f(path,size,modified)
	 LEFT JOIN LATERAL (SELECT id,owner FROM sources WHERE (path<>'' AND path=f.path) OR (thumbnail_path<>'' AND thumbnail_path=f.path) LIMIT 1) s ON true
	 LEFT JOIN LATERAL (SELECT id,owner FROM artifacts WHERE path<>'' AND path=f.path LIMIT 1) a ON true
	 ON CONFLICT(path) DO UPDATE SET owner=excluded.owner,resource_id=excluded.resource_id,kind=excluded.kind,size_bytes=GREATEST(storage_files.size_bytes,excluded.size_bytes),observed_at=GREATEST(storage_files.observed_at,excluded.observed_at)
	 WHERE storage_files.kind IN ('orphan','work_orphan') AND NOT storage_files.delete_pending
	 AND (storage_files.size_bytes<excluded.size_bytes OR storage_files.observed_at<excluded.observed_at OR storage_files.kind<>excluded.kind)`, paths, sizes, modified, kind)
	return err
}

func (s *Store) bootstrapStorage(ctx context.Context, c Config) error {
	if c.MaxStorageBytes <= 0 {
		return nil
	}
	var initialized bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_state WHERE id=$1)`, c.DataDir).Scan(&initialized); err != nil || initialized {
		return err
	}
	session := newStorageScanSession()
	defer session.close()
	for range storageProgressBatches {
		p, err := s.nextStorageScan(ctx, c, bootstrapScanKinds, false)
		if errors.Is(err, pgx.ErrNoRows) {
			// Each metadata batch commits separately, just like a directory
			// observation. Spend the remaining step budget on legacy expiry
			// backfill instead of waiting for another worker retry per 200 rows.
			err = s.finishStorageBootstrap(ctx, c)
			if errors.Is(err, ErrStorageInitializing) {
				continue
			}
			return err
		}
		if err != nil {
			return err
		}
		if err = s.storageScanStepSession(ctx, c, p, session); err != nil {
			return err
		}
	}
	return ErrStorageInitializing
}

func (s *Store) finishStorageBootstrap(ctx context.Context, c Config) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=ANY($2) AND completed_at IS NULL)`, c.DataDir, bootstrapScanKinds).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrStorageInitializing
	}
	if c.ArtifactTTL > 0 {
		result, e := tx.Exec(ctx, `UPDATE artifacts SET expires_at=created_at+($1*interval '1 second') WHERE id IN (SELECT id FROM artifacts WHERE expires_at IS NULL LIMIT $2)`, c.ArtifactTTL.Seconds(), storageBatch)
		if e != nil {
			return e
		}
		if result.RowsAffected() == storageBatch {
			if err = tx.Commit(ctx); err != nil {
				return err
			}
			return ErrStorageInitializing
		}
	}
	// Verification comes after metadata backfill: a large legacy import may
	// take several calls, during which a previously completed root can change.
	if err = refreshBootstrapRoots(ctx, tx, c); err != nil {
		return err
	}
	verified, err := verifyBootstrapDirectories(ctx, tx, c)
	if err != nil {
		return err
	}
	if !verified {
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return ErrStorageInitializing
	}
	if _, err = tx.Exec(ctx, `INSERT INTO storage_state(id) VALUES($1) ON CONFLICT DO NOTHING`, c.DataDir); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func refreshBootstrapRoots(ctx context.Context, tx pgx.Tx, c Config) error {
	for _, kind := range bootstrapScanKinds {
		path := scanRoot(c, kind)
		var mtime, size *int64
		if err := tx.QueryRow(ctx, `SELECT generation_mtime_ns,generation_size FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, kind, path).Scan(&mtime, &size); err != nil {
			return err
		}
		if err := safeScanPath(path, path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		unchanged := os.IsNotExist(err) && mtime == nil && size == nil
		if err == nil {
			unchanged = info.IsDir() && mtime != nil && size != nil && *mtime == info.ModTime().UnixNano() && *size == info.Size()
		} else if !os.IsNotExist(err) {
			return err
		}
		if !unchanged {
			if _, err = tx.Exec(ctx, `UPDATE storage_scan_progress SET position=0,prefix_digest='',generation_mtime_ns=NULL,generation_size=NULL,started_at=clock_timestamp(),completed_at=NULL,last_visited_at=clock_timestamp() WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, kind, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyBootstrapDirectories(ctx context.Context, tx pgx.Tx, c Config) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT scan_kind,path,generation_mtime_ns,generation_size FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=ANY($2) AND completed_at IS NOT NULL AND last_visited_at<completed_at ORDER BY path LIMIT $3`, c.DataDir, bootstrapScanKinds, storageScanBatchSize)
	if err != nil {
		return false, err
	}
	var directories []storageScanProgress
	for rows.Next() {
		var p storageScanProgress
		if err = rows.Scan(&p.kind, &p.path, &p.mtime, &p.size); err != nil {
			rows.Close()
			return false, err
		}
		directories = append(directories, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, err
	}
	if len(directories) == 0 {
		return true, nil
	}
	for _, p := range directories {
		if err = safeScanPath(scanRoot(c, p.kind), p.path); err != nil {
			return false, err
		}
		info, e := os.Lstat(p.path)
		unchanged := os.IsNotExist(e) && p.mtime == nil && p.size == nil
		if e == nil {
			unchanged = info.IsDir() && p.mtime != nil && p.size != nil && *p.mtime == info.ModTime().UnixNano() && *p.size == info.Size()
		} else if !os.IsNotExist(e) {
			return false, e
		}
		if unchanged {
			_, err = tx.Exec(ctx, `UPDATE storage_scan_progress SET last_visited_at=clock_timestamp() WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path)
		} else {
			_, err = tx.Exec(ctx, `UPDATE storage_scan_progress SET position=0,prefix_digest='',generation_mtime_ns=NULL,generation_size=NULL,started_at=clock_timestamp(),completed_at=NULL,last_visited_at=clock_timestamp() WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path)
		}
		if err != nil {
			return false, err
		}
	}
	// A full chunk may have more rows behind it; changed rows must be scanned
	// again. The next progress step checks both before opening admission.
	var remaining bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=ANY($2) AND (completed_at IS NULL OR last_visited_at<completed_at))`, c.DataDir, bootstrapScanKinds).Scan(&remaining)
	return !remaining, err
}
