package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) claimLegacy(ctx context.Context) (Job, string, error) {
	token := newID("lease")
	var id, owner string
	err := s.DB.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM jobs WHERE cancel_requested=false AND attempts<3 AND
 (status='queued' OR (status='running' AND lease_until<now()))
 ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1
) UPDATE jobs SET status='running',stage='preparing',message='Preparing source',lease_until=now()+interval '45 seconds',lease_token=$1,attempts=attempts+1,updated_at=now()
WHERE id=(SELECT id FROM candidate) RETURNING id,owner`, token).Scan(&id, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, "", ErrNotFound
	}
	if err != nil {
		return Job{}, "", err
	}
	j, err := s.Job(ctx, id, owner)
	return j, token, err
}

func (s *Store) Heartbeat(ctx context.Context, id, token string) (bool, error) {
	var cancelled bool
	// Lock first, then check wall-clock expiry on the materialized row. Even a
	// volatile WHERE predicate can otherwise be evaluated before a row-lock wait.
	err := s.DB.QueryRow(ctx, `WITH leased AS MATERIALIZED (
 SELECT id,lease_until FROM jobs WHERE id=$1 AND lease_token=$2 AND status='running' FOR UPDATE
) UPDATE jobs SET lease_until=clock_timestamp()+interval '45 seconds'
WHERE id IN (SELECT id FROM leased WHERE lease_until>clock_timestamp()) RETURNING cancel_requested`, id, token).Scan(&cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, ErrNotFound
	}
	return cancelled, err
}

func (s *Store) SaveJob(ctx context.Context, j Job, token string) error {
	items, _ := json.Marshal(j.Items)
	var db dbExecutor = s.DB
	var cleanupTx pgx.Tx
	if j.Status == "succeeded" && j.Request.CleanupSource {
		var err error
		cleanupTx, err = s.storageTx(ctx)
		if err != nil {
			return err
		}
		defer rollbackStorage(cleanupTx)
		db = cleanupTx
	}
	tag, err := db.Exec(ctx, `WITH leased AS MATERIALIZED (
 SELECT id,lease_until FROM jobs WHERE id=$1 AND lease_token=$2 AND status='running' FOR UPDATE
) UPDATE jobs SET
status=CASE WHEN cancel_requested AND $3 IN ('succeeded','failed','cancelled') THEN 'cancelled' ELSE $3 END,
stage=$4,message=CASE WHEN cancel_requested AND $3 IN ('succeeded','failed','cancelled') THEN 'Cancelled' ELSE $5 END,
items=CASE WHEN cancel_requested AND $3 IN ('succeeded','failed','cancelled') THEN (
 SELECT jsonb_agg(CASE WHEN item->>'status'<>'succeeded' AND EXISTS (
  SELECT 1 FROM jsonb_array_elements(jobs.items) previous
  WHERE previous->>'id'=item->>'id' AND previous->>'status' IN ('queued','running','waiting_storage')
 ) THEN item || '{"status":"cancelled","message":"Cancelled","error_code":"cancelled"}'::jsonb ELSE item END)
 FROM jsonb_array_elements($6::jsonb) item
) ELSE $6::jsonb END,updated_at=now()
WHERE id IN (SELECT id FROM leased WHERE lease_until>clock_timestamp())`, j.ID, token, j.Status, j.Stage, j.Message, items)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cleanupTx != nil {
		if err = requestSourceMediaCleanup(ctx, cleanupTx, j.ID); err != nil {
			return err
		}
		return cleanupTx.Commit(ctx)
	}
	return nil
}

// SaveJobProgress transfers only the changing progress fields. Result snapshots
// and item states are still committed by SaveJob/PublishArtifact; an old progress
// callback cannot replace them or revive a job after its lease has changed.
func (s *Store) SaveJobProgress(ctx context.Context, id, token, stage, message string, item int, progressMS int64) error {
	if item < 0 || progressMS < 0 {
		return errors.New("invalid job progress")
	}
	tag, err := s.DB.Exec(ctx, `WITH leased AS MATERIALIZED (
 SELECT id,lease_until FROM jobs WHERE id=$1 AND lease_token=$2 AND status='running' FOR UPDATE
) UPDATE jobs SET stage=$3,message=$4,
items=jsonb_set(items,ARRAY[($5::integer)::text,'progress_ms'],to_jsonb($6::bigint),true),updated_at=now()
WHERE id IN (SELECT id FROM leased WHERE lease_until>clock_timestamp())
AND $5::integer<jsonb_array_length(items) AND items->$5::integer->>'status'='running'`, id, token, stage, message, item, progressMS)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) Cancel(ctx context.Context, id, owner string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE jobs SET cancel_requested=true,status=CASE WHEN status IN ('queued','waiting_storage') THEN 'cancelled' ELSE status END,message=CASE WHEN status IN ('queued','waiting_storage') THEN 'Cancelled' ELSE message END,items=CASE WHEN status IN ('queued','waiting_storage') THEN (SELECT jsonb_agg(CASE WHEN item->>'status' IN ('queued','running','waiting_storage') THEN item || '{"status":"cancelled","message":"Cancelled","error_code":"cancelled"}'::jsonb ELSE item END) FROM jsonb_array_elements(items) item) ELSE items END,updated_at=now() WHERE id=$1 AND owner=$2 AND status IN ('queued','running','waiting_storage')`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		_, err = s.Job(ctx, id, owner)
	}
	return err
}

func (s *Store) Recover(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `UPDATE jobs SET status=CASE WHEN cancel_requested THEN 'cancelled' ELSE 'failed' END,stage='finished',lease_token=NULL,message=CASE WHEN cancel_requested THEN 'Cancelled' ELSE 'Worker recovery limit reached' END,items=(SELECT jsonb_agg(CASE WHEN item->>'status' IN ('queued','running','waiting_storage') THEN item || CASE WHEN cancel_requested THEN '{"status":"cancelled","message":"Cancelled","error_code":"cancelled"}'::jsonb ELSE '{"status":"failed","message":"Worker recovery limit reached","error_code":"server_error"}'::jsonb END ELSE item END) FROM jsonb_array_elements(items) item) WHERE status='running' AND lease_until<now() AND (attempts>=3 OR cancel_requested)`)
	return err
}
