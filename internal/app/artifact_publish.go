package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ArtifactPublicationError distinguishes a rejected publication from a lost
// commit acknowledgement. A possibly committed file must not be removed or its
// job snapshot overwritten; recovery reads the authoritative database state.
type ArtifactPublicationError struct {
	CommitUncertain bool
	cause           error
}

func (e *ArtifactPublicationError) Error() string {
	if e.CommitUncertain {
		return fmt.Sprintf("artifact publication commit outcome is unknown: %v", e.cause)
	}
	return fmt.Sprintf("artifact publication rejected: %v", e.cause)
}

func (e *ArtifactPublicationError) Unwrap() error { return e.cause }

// PublishArtifact atomically registers the result and saves the job snapshot.
// Completed file data and its known directory entries are synchronized before
// the bounded database transaction begins. The caller supplies a running job
// with exactly one succeeded item referencing a, and retains the file on an
// ArtifactPublicationError with CommitUncertain.
func (s *Store) PublishArtifact(ctx context.Context, j Job, path, token string, a Artifact) error {
	return s.publishArtifact(ctx, j, path, token, a, fsdurable.Sync)
}

func (s *Store) publishArtifact(ctx context.Context, j Job, path, token string, a Artifact, synchronize func(string, ...string) (fs.FileInfo, error)) error {
	if err := validateArtifactPublication(j, path, a); err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	c := s.storageConfig()
	var directories []string
	if c.DataDir != "" {
		if relative, err := filepath.Rel(c.DataDir, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			// The worker created artifacts under DATA_DIR. Both the artifact
			// directory entry and DATA_DIR's own entry must precede DB success.
			directories = []string{c.DataDir, filepath.Dir(c.DataDir)}
		}
	}
	info, err := synchronize(path, directories...)
	if err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	if info.Size() != a.SizeBytes {
		return &ArtifactPublicationError{cause: errors.New("artifact size does not match the synchronized file")}
	}
	if err = ctx.Err(); err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	// Synchronizing a completed file can be slow. Keep the processing deadline
	// throughout that barrier and give SQL its own budget only once it passes.
	databaseCtx, databaseCancel := context.WithTimeout(ctx, workerDatabaseTimeout)
	defer databaseCancel()
	tx, err := s.DB.Begin(databaseCtx)
	if err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	return publishArtifactTransaction(databaseCtx, tx, j, path, token, a, c)
}

func validateArtifactPublication(j Job, path string, a Artifact) error {
	if j.ID == "" || j.Owner == "" || j.Status != "running" || path == "" || a.ID == "" {
		return errors.New("invalid artifact publication snapshot")
	}
	matches := 0
	for _, item := range j.Items {
		if item.Artifact != nil && item.Artifact.ID == a.ID {
			if item.Status != "succeeded" || *item.Artifact != a {
				return errors.New("invalid artifact publication item")
			}
			matches++
		}
	}
	if matches != 1 {
		return errors.New("artifact publication must reference one completed item")
	}
	return nil
}

func publishArtifactTransaction(ctx context.Context, tx pgx.Tx, j Job, path, token string, a Artifact, configs ...Config) error {
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var c Config
	if len(configs) > 0 {
		c = configs[0]
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", storageLock); err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	items, err := json.Marshal(j.Items)
	if err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	// The row lock serializes cancellation and replacement leases with both
	// writes. Wall-clock checks also reject a lease that expires while waiting.
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM jobs WHERE id=$1 AND owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp() AND status='running' AND cancel_requested=false FOR UPDATE`, j.ID, j.Owner, token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,CASE WHEN $9::double precision>0 THEN clock_timestamp()+($9*interval '1 second') ELSE NULL END) ON CONFLICT(id) DO NOTHING`, a.ID, j.Owner, j.ID, path, a.Filename, a.SizeBytes, a.ActualStartMS, a.ActualEndMS, c.ArtifactTTL.Seconds())
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	if err = registerStorageFile(ctx, tx, path, j.Owner, "artifact", a.ID, a.SizeBytes); err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	if c.MaxStorageBytes > 0 {
		budget := c.MaxOutputBytes
		for _, item := range j.Items {
			if item.Artifact != nil && item.Artifact.ID == a.ID {
				budget = outputBudget(c, Range{StartMS: item.StartMS, EndMS: item.EndMS}, j.Request)
				break
			}
		}
		if a.SizeBytes < 0 || a.SizeBytes > budget {
			return &ArtifactPublicationError{cause: errStorageUnavailable}
		}
		changed, e := tx.Exec(ctx, "UPDATE storage_reservations SET size_bytes=size_bytes-$3 WHERE id=$1 AND token=$2 AND size_bytes>=$3", jobReservationID(j.ID, token), token, budget)
		if e != nil {
			return &ArtifactPublicationError{cause: e}
		}
		if changed.RowsAffected() == 0 {
			return &ArtifactPublicationError{cause: ErrNotFound}
		}
	}
	tag, err = tx.Exec(ctx, `UPDATE jobs SET status=$3,stage=$4,message=$5,items=$6::jsonb,updated_at=now() WHERE id=$1 AND owner=$7 AND lease_token=$2 AND lease_until>clock_timestamp() AND status='running' AND cancel_requested=false`, j.ID, token, j.Status, j.Stage, j.Message, items, j.Owner)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	if err = ctx.Err(); err != nil {
		return &ArtifactPublicationError{cause: err}
	}
	if err = tx.Commit(ctx); err != nil {
		return &ArtifactPublicationError{CommitUncertain: !publicationCommitRejected(err), cause: err}
	}
	return nil
}

func publicationCommitRejected(err error) bool {
	if errors.Is(err, pgx.ErrTxCommitRollback) || pgconn.SafeToRetry(err) {
		return true
	}
	var problem *pgconn.PgError
	if errors.As(err, &problem) {
		// Only explicit constraint/transaction rollback responses prove that
		// COMMIT failed. Transport failures and unknown resolution stay uncertain.
		return strings.HasPrefix(problem.Code, "23") || problem.Code == "40001" || problem.Code == "40P01" || problem.Code == "25P02"
	}
	return false
}
