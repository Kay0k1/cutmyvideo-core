package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	sourcePinAuditSources  = 16
	sourcePinAuditChildren = 128
	sourcePinAuditBudget   = 250 * time.Millisecond
)

// Truthful counters bypass pinned prefixes. The independent reference checks
// remain authoritative, even if an operator/runtime role corrupts a counter.
// Materializing before those checks caps visits at storageBatch. Quarantining a
// false zero gives the following batch a continuation past the bad prefix.
const sourceRetentionSQL = `WITH source_retention_candidates AS MATERIALIZED (
 SELECT source.id FROM sources source
 WHERE source.retention_pins_valid AND source.retention_job_pins=0 AND source.retention_preview_pins=0
 AND source.created_at<now()-($1*interval '1 second')
 ORDER BY source.created_at,source.id LIMIT $2
), checked AS MATERIALIZED (
 SELECT candidate.id,
 EXISTS(SELECT 1 FROM jobs WHERE source_id=candidate.id OFFSET 0)
 OR EXISTS(SELECT 1 FROM storage_reservations WHERE kind='preview' AND job_id=candidate.id) AS pinned,
 EXISTS(SELECT 1 FROM source_media_readers WHERE source_id=candidate.id AND expires_at>clock_timestamp())
 OR EXISTS(SELECT 1 FROM source_media_cleanup WHERE source_id=candidate.id) AS media_in_use
 FROM source_retention_candidates candidate
), quarantined AS (
 UPDATE sources source SET retention_pins_valid=false
 WHERE source.id=ANY(ARRAY(SELECT id FROM checked WHERE pinned)) RETURNING source.id
), deleted AS (
 DELETE FROM sources source WHERE source.id=ANY(ARRAY(SELECT id FROM checked WHERE NOT pinned AND NOT media_in_use))
 RETURNING source.path,source.thumbnail_path,source.owner
)
SELECT 'deleted'::text,path,thumbnail_path,owner,0::bigint,0::bigint FROM deleted
UNION ALL
SELECT 'progress'::text,'','','',(SELECT count(*) FROM source_retention_candidates),(SELECT count(*) FROM quarantined)`

type sourcePinAuditCursor struct {
	last, upper     pgtype.Timestamptz
	lastID, upperID *string
}

type sourcePinAuditProgress struct {
	revision                 int64
	jobCursor, previewCursor *string
	jobCount, previewCount   int64
	jobDone, previewDone     bool
}

// Audits have their own budget after media COMMIT. Each source unit commits and
// releases the storage fence before the next; cancellation preserves earlier
// units. Neither a large child set nor a repeatedly changing source holds the
// global cursor at one source. Partial counts are usable only at the captured
// membership revision, and are never used as deletion authority.
func (s *Store) auditSourcePins(ctx context.Context) (more, eligible bool, err error) {
	budget := sourcePinAuditBudget
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, max(0, time.Until(deadline)/16))
	}
	auditCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return auditSourcePinUnits(ctx, auditCtx, s.auditSourcePinUnit)
}

// Exhausting this audit's slice leaves durable work pending after media COMMIT.
// It does not prove EOF, a repaired counter, or the outcome of a failed COMMIT.
func auditSourcePinUnits(ctx, auditCtx context.Context, unit func(context.Context) (bool, bool, error)) (more, eligible bool, err error) {
	for range sourcePinAuditSources {
		more = true
		if err = ctx.Err(); err != nil {
			return more, eligible, fmt.Errorf("source pin audit: %w", err)
		}
		done, repaired, unitErr := unit(auditCtx)
		if unitErr != nil {
			// Inspect the original error before joining context diagnostics: a
			// database/connection error coinciding with expiry must stay visible.
			if ctx.Err() == nil && auditCtx.Err() == context.DeadlineExceeded && sourcePinAuditDeadlineOnly(unitErr) {
				return more, eligible, nil
			}
			return more, eligible, fmt.Errorf("source pin audit: %w", errors.Join(ctx.Err(), unitErr))
		}
		eligible = eligible || repaired
		more = !done
		if err = ctx.Err(); err != nil {
			return more, eligible, fmt.Errorf("source pin audit: %w", err)
		}
		if done {
			break
		}
	}
	return more, eligible, nil
}

// A mixed error tree is not an owned-budget outcome merely because one branch
// matches DeadlineExceeded. Only wrappers around that exact cause are deferred.
func sourcePinAuditDeadlineOnly(err error) bool {
	if err == context.DeadlineExceeded {
		return true
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !sourcePinAuditDeadlineOnly(cause) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return sourcePinAuditDeadlineOnly(e.Unwrap())
	default:
		return false
	}
}

func (s *Store) auditSourcePinUnit(ctx context.Context) (done, eligible bool, err error) {
	return s.auditSourcePinUnitCommit(ctx, func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) })
}

// Tests can lose an acknowledgement after a real COMMIT or a real rollback.
// Production always uses pgx Commit; subsequent units read durable progress.
func (s *Store) auditSourcePinUnitCommit(ctx context.Context, commit func(context.Context, pgx.Tx) error) (done, eligible bool, err error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return false, false, err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='100ms'"); err != nil {
		return false, false, err
	}
	// Missing progress is a restart, never an assertion that all sources passed.
	if _, err = tx.Exec(ctx, `INSERT INTO source_retention_audit(id) VALUES(1) ON CONFLICT(id) DO NOTHING`); err != nil {
		return false, false, err
	}
	var cursor sourcePinAuditCursor
	if err = tx.QueryRow(ctx, `SELECT last_created_at,last_source_id,upper_created_at,upper_source_id FROM source_retention_audit WHERE id=1 FOR UPDATE`).Scan(&cursor.last, &cursor.lastID, &cursor.upper, &cursor.upperID); err != nil {
		return false, false, err
	}
	if !cursor.upper.Valid {
		err = tx.QueryRow(ctx, `SELECT created_at,id FROM sources ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&cursor.upper, &cursor.upperID)
		if errors.Is(err, pgx.ErrNoRows) {
			return true, false, commit(ctx, tx)
		}
		if err != nil {
			return false, false, err
		}
		cursor.last = pgtype.Timestamptz{}
		cursor.lastID = nil
	}
	var id string
	var created pgtype.Timestamptz
	var revision int64
	// Separate first/resume queries preserve index range seeks: no nullable OR
	// filter may replay all earlier rows before applying the tuple comparison.
	if cursor.last.Valid {
		err = tx.QueryRow(ctx, `SELECT id,created_at,retention_pin_revision FROM sources WHERE (created_at,id)>($1,$2) AND (created_at,id)<=($3,$4) ORDER BY created_at,id LIMIT 1`, cursor.last, cursor.lastID, cursor.upper, cursor.upperID).Scan(&id, &created, &revision)
	} else {
		err = tx.QueryRow(ctx, `SELECT id,created_at,retention_pin_revision FROM sources WHERE (created_at,id)<=($1,$2) ORDER BY created_at,id LIMIT 1`, cursor.upper, cursor.upperID).Scan(&id, &created, &revision)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = tx.Exec(ctx, `UPDATE source_retention_audit SET last_created_at=NULL,last_source_id=NULL,upper_created_at=NULL,upper_source_id=NULL WHERE id=1`); err != nil {
			return false, false, err
		}
		return true, false, commit(ctx, tx)
	}
	if err != nil {
		return false, false, err
	}
	var progress sourcePinAuditProgress
	err = tx.QueryRow(ctx, `SELECT revision,job_cursor,job_count,job_done,preview_cursor,preview_count,preview_done FROM source_pin_audit_progress WHERE source_id=$1`, id).Scan(&progress.revision, &progress.jobCursor, &progress.jobCount, &progress.jobDone, &progress.previewCursor, &progress.previewCount, &progress.previewDone)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && progress.revision != revision) {
		progress = sourcePinAuditProgress{revision: revision}
	} else if err != nil {
		return false, false, err
	}
	if !progress.jobDone {
		progress.jobCursor, progress.jobCount, progress.jobDone, err = auditSourcePinChildren(ctx, tx, id, false, progress.jobCursor, progress.jobCount)
		if err != nil {
			return false, false, err
		}
	}
	if !progress.previewDone {
		progress.previewCursor, progress.previewCount, progress.previewDone, err = auditSourcePinChildren(ctx, tx, id, true, progress.previewCursor, progress.previewCount)
		if err != nil {
			return false, false, err
		}
	}
	if progress.jobDone && progress.previewDone {
		result, e := tx.Exec(ctx, `UPDATE sources SET retention_job_pins=$2,retention_preview_pins=$3,retention_pins_valid=true WHERE id=$1 AND retention_pin_revision=$4 AND (NOT retention_pins_valid OR retention_job_pins<>$2 OR retention_preview_pins<>$3)`, id, progress.jobCount, progress.previewCount, revision)
		if e != nil {
			return false, false, e
		}
		eligible = result.RowsAffected() > 0 && progress.jobCount == 0 && progress.previewCount == 0
		if _, err = tx.Exec(ctx, `DELETE FROM source_pin_audit_progress WHERE source_id=$1`, id); err != nil {
			return false, false, err
		}
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO source_pin_audit_progress(source_id,revision,job_cursor,job_count,job_done,preview_cursor,preview_count,preview_done) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(source_id) DO UPDATE SET revision=excluded.revision,job_cursor=excluded.job_cursor,job_count=excluded.job_count,job_done=excluded.job_done,preview_cursor=excluded.preview_cursor,preview_count=excluded.preview_count,preview_done=excluded.preview_done`, id, progress.revision, progress.jobCursor, progress.jobCount, progress.jobDone, progress.previewCursor, progress.previewCount, progress.previewDone)
		if err != nil {
			return false, false, err
		}
	}
	// Advance even when this source remains partial. A completed sweep does not
	// mean partial children have been fully audited; their rows persist for wrap.
	if _, err = tx.Exec(ctx, `UPDATE source_retention_audit SET last_created_at=$1,last_source_id=$2,upper_created_at=$3,upper_source_id=$4 WHERE id=1`, created, id, cursor.upper, cursor.upperID); err != nil {
		return false, false, err
	}
	return false, eligible, commit(ctx, tx)
}

func auditSourcePinChildren(ctx context.Context, tx pgx.Tx, id string, preview bool, cursor *string, count int64) (next *string, total int64, done bool, err error) {
	// Four literal SQL statements keep both membership and cursor range indexed.
	query := `SELECT id FROM jobs WHERE source_id=$1 ORDER BY id LIMIT $2`
	args := []any{id, sourcePinAuditChildren + 1}
	if preview {
		query = `SELECT id FROM storage_reservations WHERE kind='preview' AND job_id=$1 ORDER BY id LIMIT $2`
	}
	if cursor != nil {
		query = `SELECT id FROM jobs WHERE source_id=$1 AND id>$2 ORDER BY id LIMIT $3`
		if preview {
			query = `SELECT id FROM storage_reservations WHERE kind='preview' AND job_id=$1 AND id>$2 ORDER BY id LIMIT $3`
		}
		args = []any{id, *cursor, sourcePinAuditChildren + 1}
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, 0, false, err
	}
	visited := min(len(ids), sourcePinAuditChildren)
	next = cursor
	if visited > 0 {
		last := ids[visited-1]
		next = &last
	}
	return next, count + int64(visited), len(ids) <= sourcePinAuditChildren, nil
}
