package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

// Every admission, publication and metadata deletion takes this stable lock
// before resource row locks. It is shared by API and worker processes.
const storageLock int64 = 736021912360105
const storageBatch = 200
const storageSchema = `
CREATE TABLE IF NOT EXISTS storage_files (
 path text PRIMARY KEY, owner text NOT NULL DEFAULT '', resource_id text NOT NULL DEFAULT '',
 kind text NOT NULL, size_bytes bigint NOT NULL CHECK(size_bytes>=0),
 delete_pending boolean NOT NULL DEFAULT false, observed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS storage_files_owner_idx ON storage_files(owner,kind);
CREATE INDEX IF NOT EXISTS sources_owner_created_idx ON sources(owner,created_at DESC);
CREATE INDEX IF NOT EXISTS storage_files_pending_idx ON storage_files(path) WHERE delete_pending;
CREATE TABLE IF NOT EXISTS storage_reservations (
 id text PRIMARY KEY, owner text NOT NULL, kind text NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes>=0), expires_at timestamptz NOT NULL,
 token text NOT NULL DEFAULT '',job_id text NOT NULL DEFAULT '',abandoned_at timestamptz
);
ALTER TABLE storage_reservations ADD COLUMN IF NOT EXISTS job_id text NOT NULL DEFAULT '';
ALTER TABLE storage_reservations ADD COLUMN IF NOT EXISTS abandoned_at timestamptz;
CREATE INDEX IF NOT EXISTS storage_reservations_job_idx ON storage_reservations(job_id);
CREATE INDEX IF NOT EXISTS storage_reservations_expiry_idx ON storage_reservations(expires_at);
CREATE TABLE IF NOT EXISTS storage_counters (id integer PRIMARY KEY CHECK(id=1), stored_bytes numeric NOT NULL CHECK(stored_bytes>=0),reserved_bytes numeric NOT NULL CHECK(reserved_bytes>=0));
INSERT INTO storage_counters(id,stored_bytes,reserved_bytes)
 SELECT 1,COALESCE((SELECT sum(size_bytes) FROM storage_files),0),COALESCE((SELECT sum(size_bytes) FROM storage_reservations),0) ON CONFLICT(id) DO NOTHING;
CREATE OR REPLACE FUNCTION update_storage_counter() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE delta numeric; BEGIN
 IF TG_OP='INSERT' THEN SELECT COALESCE(sum(size_bytes),0) INTO delta FROM storage_new;
 ELSIF TG_OP='DELETE' THEN SELECT -COALESCE(sum(size_bytes),0) INTO delta FROM storage_old;
 ELSE SELECT COALESCE((SELECT sum(size_bytes) FROM storage_new),0)-COALESCE((SELECT sum(size_bytes) FROM storage_old),0) INTO delta; END IF;
 IF delta<>0 THEN
  IF TG_TABLE_NAME='storage_files' THEN UPDATE storage_counters SET stored_bytes=stored_bytes+delta WHERE id=1;
  ELSE UPDATE storage_counters SET reserved_bytes=reserved_bytes+delta WHERE id=1; END IF;
 END IF;
 RETURN NULL; END $$;
DROP TRIGGER IF EXISTS storage_file_counter ON storage_files;
DROP TRIGGER IF EXISTS storage_reservation_counter ON storage_reservations;
CREATE OR REPLACE TRIGGER storage_file_insert AFTER INSERT ON storage_files REFERENCING NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_file_update AFTER UPDATE ON storage_files REFERENCING OLD TABLE AS storage_old NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_file_delete AFTER DELETE ON storage_files REFERENCING OLD TABLE AS storage_old FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_reservation_insert AFTER INSERT ON storage_reservations REFERENCING NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_reservation_update AFTER UPDATE ON storage_reservations REFERENCING OLD TABLE AS storage_old NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_reservation_delete AFTER DELETE ON storage_reservations REFERENCING OLD TABLE AS storage_old FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE TABLE IF NOT EXISTS storage_state (id text PRIMARY KEY);
CREATE TABLE IF NOT EXISTS queue_owners (owner text PRIMARY KEY,last_claimed timestamptz,last_considered timestamptz);
ALTER TABLE queue_owners ALTER COLUMN last_claimed DROP NOT NULL;
ALTER TABLE queue_owners ADD COLUMN IF NOT EXISTS last_considered timestamptz;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS storage_wait_until timestamptz;
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS expires_at timestamptz;
`

var ErrSourceCommitUncertain = errors.New("source publication commit outcome unknown")
var ErrSourceInUse = errors.New("source has active jobs")
var errStorageUnavailable = errors.New("storage unavailable")

func (s *Store) ConfigureStorage(c Config) { s.configMu.Lock(); s.config = c; s.configMu.Unlock() }
func (s *Store) storageConfig() Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}
func rollbackStorage(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func (s *Store) storageTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", storageLock); err != nil {
		rollbackStorage(tx)
		return nil, err
	}
	return tx, nil
}

// Bootstrap adopts existing media once. It never deletes data, and retries the
// whole transaction if interrupted. Normal admissions then read indexed totals.
func (s *Store) bootstrapStorage(ctx context.Context, tx pgx.Tx, c Config) error {
	if c.MaxStorageBytes <= 0 {
		return nil
	}
	var initialized bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_state WHERE id=$1)", c.DataDir).Scan(&initialized); err != nil {
		return err
	}
	if initialized {
		return nil
	}
	for _, kind := range []string{"sources", "artifacts"} {
		if err := bootstrapStorageDirectory(ctx, tx, filepath.Join(c.DataDir, kind)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Legacy workspaces had no durable reservation. Charge their actual files
	// until fenced cleanup removes the workspace; never delete them at bootstrap.
	if err := bootstrapLegacyWork(ctx, tx, filepath.Join(c.DataDir, "work")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Adopt ownership for files from the previous release, including thumbnails.
	_, err := tx.Exec(ctx, `UPDATE storage_files f SET owner=s.owner,resource_id=s.id,kind='source' FROM sources s WHERE f.path=s.path OR f.path=s.thumbnail_path;
 UPDATE storage_files f SET owner=a.owner,resource_id=a.id,kind='artifact' FROM artifacts a WHERE f.path=a.path;`)
	if err != nil {
		return err
	}
	if c.ArtifactTTL > 0 {
		if _, err = tx.Exec(ctx, "UPDATE artifacts SET expires_at=created_at+($1*interval '1 second') WHERE expires_at IS NULL", c.ArtifactTTL.Seconds()); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO storage_state(id) VALUES($1) ON CONFLICT DO NOTHING", c.DataDir)
	return err
}
func storageFits(ctx context.Context, tx pgx.Tx, c Config, additional int64, exclude string) (bool, error) {
	if additional < 0 || c.StorageSafetyBytes < 0 || additional > c.MaxStorageBytes {
		return false, nil
	}
	if additional == 0 {
		return true, nil
	}
	var logicalOK bool
	var physical int64
	// Transactional numeric counters are one row, independent of retained file
	// count. Numeric arithmetic cannot wrap, even when reservations are huge.
	if err := tx.QueryRow(ctx, `SELECT stored_bytes+reserved_bytes-COALESCE((SELECT size_bytes FROM storage_reservations WHERE id=$1),0)+$2::numeric<=$3::numeric,LEAST(reserved_bytes-COALESCE((SELECT size_bytes FROM storage_reservations WHERE id=$1),0),9223372036854775807)::bigint FROM storage_counters WHERE id=1`, exclude, additional, c.MaxStorageBytes).Scan(&logicalOK, &physical); err != nil {
		return false, err
	}
	if !logicalOK {
		return false, nil
	}
	if physical > math.MaxInt64-additional || physical+additional > math.MaxInt64-c.StorageSafetyBytes {
		return false, nil
	}
	return filesystemStorageAvailable(c.DataDir, physical+additional+c.StorageSafetyBytes), nil
}
func registerStorageFile(ctx context.Context, tx pgx.Tx, path, owner, kind, id string, size int64) error {
	if path == "" {
		return nil
	}
	if size < 0 {
		return errors.New("negative stored file size")
	}
	_, err := tx.Exec(ctx, `INSERT INTO storage_files(path,owner,kind,resource_id,size_bytes) VALUES($1,$2,$3,$4,$5) ON CONFLICT(path) DO UPDATE SET owner=excluded.owner,kind=excluded.kind,resource_id=excluded.resource_id,size_bytes=excluded.size_bytes,delete_pending=false`, path, owner, kind, id, size)
	return err
}
func observedSize(path string, fallback int64) int64 {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return info.Size()
	}
	return max(0, fallback)
}

func (s *Store) ReserveSource(ctx context.Context, c Config, owner string, tokens ...string) error {
	return s.reserveSource(ctx, c, owner, sourceReservationToken(tokens), false)
}
func (s *Store) reserveSource(ctx context.Context, c Config, owner, token string, thumbnail bool, sizes ...int64) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if err = s.bootstrapStorage(ctx, tx, c); err != nil {
		return err
	}
	// Expired preparations may still have files: reconciliation adopts these
	// before releasing their reservation in maintenance, never here.
	var busy bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE id=$1)", "source:"+owner).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errSourceBusy
	}
	var active, count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM storage_reservations WHERE kind='source'").Scan(&active); err != nil {
		return err
	}
	if active >= 4 {
		return errSourceServerBusy
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM sources WHERE owner=$1", owner).Scan(&count); err != nil {
		return err
	}
	if count >= 20 {
		return errSourceLimit
	}
	reserve := c.MaxSourceBytes
	if len(sizes) > 0 && sizes[0] > 0 {
		reserve = min(reserve, sizes[0])
	}
	if thumbnail {
		reserve = maxThumbnailBytes
	}
	var ownedOK bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(size_bytes),0)+$2::numeric<=$3::numeric FROM storage_files WHERE owner=$1 AND kind='source'`, owner, reserve, c.MaxOwnerBytes).Scan(&ownedOK); err != nil {
		return err
	}
	if !ownedOK {
		return errSourceStorage
	}
	fits, err := storageFits(ctx, tx, c, reserve, "")
	if err != nil {
		return err
	}
	if !fits {
		return errSourceStorage
	}
	if c.SourceTimeout > time.Duration(math.MaxInt64)-time.Minute {
		return errSourceStorage
	}
	ttl := max(c.SourceTimeout, uploadTimeout(c)) + time.Minute
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if _, err = tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,token) VALUES($1,$2,'source',$3,clock_timestamp()+($4*interval '1 second'),$5)`, "source:"+owner, owner, reserve, ttl.Seconds(), token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ReleaseSource(ctx context.Context, owner string, tokens ...string) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND token=$2", "source:"+owner, sourceReservationToken(tokens)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AddSource(ctx context.Context, v Source) error {
	v.Title = normalizeSourceTitle(v.Title)
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	_, err = tx.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,path,url,width,height,embed_url,thumbnail_url,provider_id,provider,thumbnail_path) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, v.ID, v.Owner, v.Title, v.DurationMS, v.Kind, v.Path, v.URL, v.Width, v.Height, v.EmbedURL, v.ThumbnailURL, v.ProviderID, v.Provider, v.ThumbnailPath)
	if err != nil {
		return err
	}
	for _, path := range []string{v.Path, v.ThumbnailPath} {
		if path == "" {
			continue
		}
		if err = registerStorageFile(ctx, tx, path, v.Owner, "source", v.ID, observedSize(path, 0)); err != nil {
			return err
		}
	}
	if v.StorageToken != "" {
		if err = adoptPreparationExtras(ctx, tx, s.storageConfig(), v.ID); err != nil {
			return err
		}
	}
	if v.StorageToken != "" {
		tag, e := tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND token=$2 AND expires_at>clock_timestamp()", "source:"+v.Owner, v.StorageToken)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	if err = tx.Commit(ctx); err != nil && !publicationCommitRejected(err) {
		return errors.Join(ErrSourceCommitUncertain, err)
	}
	return err
}
func remainingJobReserve(c Config, j Job, platform bool) (int64, error) {
	var count int64
	for _, item := range j.Items {
		if item.Status != "succeeded" || item.Artifact == nil {
			count++
		}
	}
	if c.MaxOutputBytes <= 0 || count > math.MaxInt64/c.MaxOutputBytes {
		return 0, errStorageUnavailable
	}
	size := count * c.MaxOutputBytes
	if count == 0 {
		return 0, nil
	}
	if platform {
		if remoteSourceBudget(c) < 0 || remoteSourceBudget(c) > (math.MaxInt64-size)/2 {
			return 0, errStorageUnavailable
		}
		size += 2 * remoteSourceBudget(c)
	}
	return size, nil
}
func (s *Store) ReleaseJobStorage(ctx context.Context, id, token string) error {
	if err := workspaceRemoved(s.storageConfig(), id, token); err != nil {
		return err
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND token=$2", jobReservationID(id, token), token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) WaitForStorage(ctx context.Context, id, token string, c Config) error {
	if err := workspaceRemoved(c, id, token); err != nil {
		return err
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	timeout := c.StorageWaitTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	tag, err := tx.Exec(ctx, `UPDATE jobs SET status='waiting_storage',stage='waiting_storage',message='Waiting for free storage',storage_wait_until=COALESCE(storage_wait_until,clock_timestamp()+($3*interval '1 second')),lease_token=NULL,lease_until=NULL,attempts=GREATEST(0,attempts-1),updated_at=now() WHERE id=$1 AND lease_token=$2 AND status='running' AND lease_until>clock_timestamp()`, id, token, timeout.Seconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// The caller removes its temporary workspace before invoking this method.
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND token=$2", jobReservationID(id, token), token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func storagePathInside(dir, path string) bool {
	base, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != "." && rel != ".." && len(rel) > 0 && rel[:min(3, len(rel))] != "../"
}
func storageFailureItems(j Job, code, message string) []byte {
	for i := range j.Items {
		if j.Items[i].Status == "queued" || j.Items[i].Status == "running" {
			j.Items[i].Status = "failed"
			j.Items[i].ErrorCode = code
			j.Items[i].Message = message
		}
	}
	return mustMarshalItems(j.Items)
}
func mustMarshalItems(items []JobItem) []byte { b, _ := json.Marshal(items); return b }

func sourceReservationToken(tokens []string) string {
	if len(tokens) > 0 {
		return tokens[0]
	}
	return ""
}

func jobReservationID(id, token string) string { return "job:" + id + ":" + token }

func adoptPreparationExtras(ctx context.Context, tx pgx.Tx, c Config, id string) error {
	if id == "" {
		return nil
	}
	for _, suffix := range []string{".media", ".thumbnail"} {
		path := filepath.Join(c.DataDir, "sources", id+suffix)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unexpected non-regular preparation file")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,observed_at) VALUES($1,'orphan',$2,$3) ON CONFLICT(path) DO NOTHING`, path, info.Size(), info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) FinishSource(ctx context.Context, c Config, owner, token, id string) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if err = adoptPreparationExtras(ctx, tx, c, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND token=$2", "source:"+owner, token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func bootstrapStorageDirectory(ctx context.Context, tx pgx.Tx, path string) error {
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
		var paths []string
		var sizes []int64
		var modified []time.Time
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			info, e := entry.Info()
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			paths = append(paths, filepath.Join(path, entry.Name()))
			sizes = append(sizes, info.Size())
			modified = append(modified, info.ModTime())
		}
		if len(paths) > 0 {
			if _, err = tx.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,observed_at) SELECT path,'orphan',size,modified FROM unnest($1::text[],$2::bigint[],$3::timestamptz[]) AS f(path,size,modified) ON CONFLICT(path) DO NOTHING`, paths, sizes, modified); err != nil {
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

func bootstrapLegacyWork(ctx context.Context, tx pgx.Tx, dir string) error {
	return scanStorageDirectory(ctx, dir, func(path string, entry os.DirEntry) error {
		if entry.IsDir() {
			var reserved bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE kind='job' AND job_id||'-'||token=$1)", entry.Name()).Scan(&reserved); err != nil {
				return err
			}
			if reserved {
				return nil
			}
			return bootstrapLegacyWork(ctx, tx, path)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,observed_at) VALUES($1,'work_orphan',$2,$3) ON CONFLICT(path) DO NOTHING`, path, info.Size(), info.ModTime())
		return err
	})
}

func workspaceRemoved(c Config, id, token string) error {
	if c.DataDir == "" {
		return nil
	}
	_, err := os.Lstat(filepath.Join(c.DataDir, "work", id+"-"+token))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("workspace still retains files")
}
