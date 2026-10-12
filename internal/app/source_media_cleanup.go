package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSourceMediaRemoved is checked only after owner authorization. A retained
// source remains a project/result reference, never permission to refetch media.
var ErrSourceMediaRemoved = errors.New("source media has been removed; reopen the recording to continue editing")

func checkSourceMedia(ctx context.Context, db dbExecutor, id, owner string) error {
	var removed, pending bool
	err := db.QueryRow(ctx, `SELECT media_removed,EXISTS(SELECT 1 FROM source_media_cleanup WHERE source_id=sources.id) FROM sources WHERE id=$1 AND owner=$2`, id, owner).Scan(&removed, &pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if removed || pending {
		return ErrSourceMediaRemoved
	}
	return nil
}

// Scheduling commits with the fully-successful terminal snapshot. Capture the
// accepted active work and newer jobs, rather than letting historical failed
// jobs permanently prevent cleanup after a successful retry.
func requestSourceMediaCleanup(ctx context.Context, tx pgx.Tx, jobID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO source_media_cleanup(source_id,successful_job_id,protected_job_ids)
 SELECT j.source_id,j.id,ARRAY(SELECT other.id FROM jobs other WHERE other.source_id=j.source_id AND (other.created_at>=j.created_at OR other.updated_at>=j.created_at OR other.status IN ('queued','running','waiting_storage')))
 FROM jobs j JOIN sources source ON source.id=j.source_id
 WHERE j.id=$1 AND j.status='succeeded' AND NOT j.cancel_requested AND NOT source.media_removed
 AND j.request->'cleanup_source'='true'::jsonb
 AND jsonb_array_length(j.items)>0
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(j.items) item WHERE item->>'status' IS DISTINCT FROM 'succeeded' OR item->'artifact' IS NULL OR item->'artifact'='null'::jsonb OR NOT EXISTS(SELECT 1 FROM artifacts artifact WHERE artifact.id=item->'artifact'->>'id' AND artifact.job_id=j.id AND artifact.owner=j.owner))
 ON CONFLICT(source_id) DO NOTHING`, jobID)
	return err
}

// ProcessSourceMediaCleanup runs small units under the common admission fence.
// No source/job/artifact cascade is involved. The durable request is retained
// on errors; physical persistent files use the existing charged tombstone path.
func (s *Store) ProcessSourceMediaCleanup(ctx context.Context, c Config) error {
	rows, err := s.DB.Query(ctx, `SELECT source_id FROM source_media_cleanup ORDER BY requested_at,source_id LIMIT 16`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		paths, err := s.prepareSourceMediaCleanup(ctx, c, id)
		if err != nil {
			return err
		}
		if err = s.DrainStorageDeletes(ctx, c, paths); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) prepareSourceMediaCleanup(ctx context.Context, c Config, id string) ([]string, error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackStorage(tx)
	var owner, path, thumbnail string
	var protected []string
	err = tx.QueryRow(ctx, `SELECT source.owner,source.path,source.thumbnail_path,cleanup.protected_job_ids FROM source_media_cleanup cleanup JOIN sources source ON source.id=cleanup.source_id WHERE source.id=$1 FOR UPDATE OF source,cleanup`, id).Scan(&owner, &path, &thumbnail, &protected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var failed, active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE id=ANY($2) AND status IN ('failed','cancelled')),EXISTS(SELECT 1 FROM jobs WHERE source_id=$1 AND status IN ('queued','running','waiting_storage')) OR EXISTS(SELECT 1 FROM storage_reservations WHERE job_id=$1 AND kind='preview') OR EXISTS(SELECT 1 FROM storage_reservations reservation JOIN jobs job ON job.id=reservation.job_id WHERE job.source_id=$1 AND reservation.kind='job') OR EXISTS(SELECT 1 FROM source_media_readers WHERE source_id=$1 AND expires_at>clock_timestamp())`, id, protected).Scan(&failed, &active); err != nil {
		return nil, err
	}
	if failed {
		if _, err = tx.Exec(ctx, `DELETE FROM source_media_cleanup WHERE source_id=$1`, id); err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if active {
		return nil, nil
	}
	// An expired reader cannot issue further reads: its context is cancelled
	// before its last acknowledged lease expires. Rows from crashes are bounded.
	if _, err = tx.Exec(ctx, `DELETE FROM source_media_readers WHERE source_id=$1 AND expires_at<=clock_timestamp()`, id); err != nil {
		return nil, err
	}
	// Generic preview-directory removal includes video, storyboard and waveform
	// entries without depending on the cache key or asset filename.
	if err = deleteSourcePreviews(ctx, tx, c, id); err != nil {
		return nil, err
	}
	files := []storageDeletionFile{{path: path, owner: owner}, {path: thumbnail, owner: owner}}
	if err = tombstoneStorageFiles(ctx, tx, files); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sources SET path='',thumbnail_path='',thumbnail_url=NULL,media_removed=true WHERE id=$1`, id); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM source_media_cleanup WHERE source_id=$1`, id); err != nil {
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

// Expired crashed readers are independent of a source's retention lifetime.
// One indexed batch leaves active readers and rows locked by renewal untouched.
func (s *Store) cleanupSourceMediaReaders(ctx context.Context) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='750ms'"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH expired AS MATERIALIZED (
 SELECT id FROM source_media_readers WHERE expires_at<=now()
 ORDER BY expires_at,id LIMIT $1 FOR UPDATE SKIP LOCKED
 ) DELETE FROM source_media_readers reader USING expired
 WHERE reader.id=expired.id AND reader.expires_at<=now()`, storageBatch)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const sourceReaderTTL = time.Minute

// Native HTTP reads are independent of expensive preview decoder admissions.
// Bounded renewable leases protect an in-flight read across API processes;
// contextReadSeeker stops reads if renewal becomes uncertain or expires.
func (s *Store) acquireSourceMediaReader(ctx context.Context, source Source, id string) (time.Time, error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer rollbackStorage(tx)
	if err = checkSourceMedia(ctx, tx, source.ID, source.Owner); err != nil {
		return time.Time{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM source_media_readers WHERE expires_at>now() ORDER BY expires_at,id LIMIT 128) bounded`).Scan(&count); err != nil {
		return time.Time{}, err
	}
	if count >= 128 {
		return time.Time{}, errSourceServerBusy
	}
	// Using the client's clock makes the read deadline conservative if the
	// database wall clock differs; PostgreSQL remains lease authority.
	deadline := time.Now().Add(sourceReaderTTL - 10*time.Second)
	if _, err = tx.Exec(ctx, `INSERT INTO source_media_readers(id,source_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '60 seconds')`, id, source.ID); err != nil {
		return time.Time{}, err
	}
	return deadline, commitPreviewAdmission(ctx, tx)
}

func (s *Server) pinSourceMediaReader(parent context.Context, source Source) (context.Context, func(), error) {
	id := newID("source_read")
	deadline, err := apiDatabase(parent, func(ctx context.Context) (time.Time, error) { return s.Store.acquireSourceMediaReader(ctx, source, id) })
	releaseRow := func() {
		ctx, cancel := context.WithTimeout(context.Background(), workerDatabaseTimeout)
		defer cancel()
		_ = apiDatabaseExec(ctx, func(ctx context.Context) error {
			_, err := s.Store.DB.Exec(ctx, `DELETE FROM source_media_readers WHERE id=$1 AND source_id=$2`, id, source.ID)
			return err
		})
	}
	if err != nil {
		if errors.Is(err, errPreviewAdmissionUncertain) {
			releaseRow()
		}
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		expires := time.NewTimer(time.Until(deadline))
		defer expires.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-expires.C:
				cancel()
				return
			case <-ticker.C:
				next := time.Now().Add(sourceReaderTTL - 10*time.Second)
				err := apiDatabaseExec(ctx, func(ctx context.Context) error {
					tag, err := s.Store.DB.Exec(ctx, `UPDATE source_media_readers SET expires_at=clock_timestamp()+interval '60 seconds' WHERE id=$1 AND source_id=$2 AND expires_at>clock_timestamp()`, id, source.ID)
					if err == nil && tag.RowsAffected() != 1 {
						return ErrNotFound
					}
					return err
				})
				if err != nil {
					cancel()
					return
				}
				if !expires.Stop() {
					select {
					case <-expires.C:
					default:
					}
				}
				expires.Reset(time.Until(next))
			}
		}
	}()
	return ctx, func() { cancel(); <-done; releaseRow() }, nil
}

type contextReadSeeker struct {
	context context.Context
	io.ReadSeeker
}

func (r contextReadSeeker) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Read(p)
}
func (r contextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Seek(offset, whence)
}

func sourceMediaError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSourceMediaRemoved) {
		writeError(w, http.StatusGone, "source_removed", "The original recording was removed after export. Your project and finished clips remain available; reopen the recording to continue editing.")
		return
	}
	if problem := problemFromError(err); problem != nil {
		status := http.StatusTooManyRequests
		if errors.Is(err, errSourceServerBusy) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, problem.code, problem.message)
		return
	}
	lookupError(w, err)
}
