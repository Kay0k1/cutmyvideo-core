package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateJobLimited(ctx context.Context, owner string, r ExportRequest, key string, limit, globalLimit int) (Job, error) {
	return s.createJobLimited(ctx, owner, r, key, limit, globalLimit, nil)
}

func (s *Store) createJobLimited(ctx context.Context, owner string, r ExportRequest, key string, limit, globalLimit int, binding *projectJobBinding) (Job, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Job{}, err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(736021912360105)`); err != nil {
		return Job{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, owner); err != nil {
		return Job{}, err
	}
	if key != "" {
		var id string
		err = tx.QueryRow(ctx, `SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2`, owner, key).Scan(&id)
		if err == nil {
			job, err := readJob(ctx, tx, id, owner)
			if err != nil || binding == nil {
				return job, err
			}
			if err = bindScopedProjectJob(ctx, tx, *binding, owner, job); err != nil {
				return Job{}, err
			}
			return job, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Job{}, err
		}
	}
	var sourceID, sourceKind string
	if err = tx.QueryRow(ctx, "SELECT id,kind FROM sources WHERE id=$1 AND owner=$2 FOR KEY SHARE", r.SourceID, owner).Scan(&sourceID, &sourceKind); errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if err = checkSourceMedia(ctx, tx, sourceID, owner); err != nil {
		return Job{}, err
	}
	c := s.storageConfig()
	if c.MaxStorageBytes > 0 {
		items := jobItems(r)
		reserve, e := remainingJobReserve(c, Job{Items: items, Request: r}, sourceKind == "platform" || sourceKind == "youtube")
		if e != nil || reserve > c.MaxStorageBytes {
			return Job{}, ErrJobTooLarge
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE owner=$1 AND status IN ('queued','running','waiting_storage')`, owner).Scan(&count); err != nil {
		return Job{}, err
	}
	if count >= limit {
		return Job{}, ErrBusy
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status IN ('queued','running','waiting_storage')`).Scan(&count); err != nil {
		return Job{}, err
	}
	if count >= globalLimit {
		return Job{}, ErrBusy
	}
	j, err := insertJob(ctx, tx, owner, r, key)
	if err != nil {
		return j, err
	}
	// A scoped job becomes visible to its recipient in the same commit that
	// admits it. Refused/expired scopes and full histories cannot leave queued
	// work that the recipient cannot recover through the project.
	if binding != nil {
		if err = bindScopedProjectJob(ctx, tx, *binding, owner, j); err != nil {
			return Job{}, err
		}
	}
	err = tx.Commit(ctx)
	return j, err
}

func (s *Store) CreateJob(ctx context.Context, owner string, r ExportRequest, key string) (Job, error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return Job{}, err
	}
	defer rollbackStorage(tx)
	if key != "" {
		var id string
		err = tx.QueryRow(ctx, "SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2", owner, key).Scan(&id)
		if err == nil {
			return readJob(ctx, tx, id, owner)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Job{}, err
		}
	}
	if err = checkSourceMedia(ctx, tx, r.SourceID, owner); err != nil {
		return Job{}, err
	}
	j, err := insertJob(ctx, tx, owner, r, key)
	if err != nil {
		return j, err
	}
	return j, tx.Commit(ctx)
}

func insertJob(ctx context.Context, db dbExecutor, owner string, r ExportRequest, key string) (Job, error) {
	j := Job{ID: newID("job"), Owner: owner, Status: "queued", Stage: "queued", Message: "Waiting for a worker", Request: r, Items: jobItems(r)}
	request, _ := json.Marshal(r)
	items, _ := json.Marshal(j.Items)
	var idem *string
	if key != "" {
		idem = &key
	}
	_, err := db.Exec(ctx, `INSERT INTO jobs(id,owner,source_id,request,items,idempotency_key) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(owner,idempotency_key) DO NOTHING`, j.ID, owner, r.SourceID, request, items, idem)
	if err != nil {
		return j, err
	}
	// NOTIFY is delivered on commit; workers retain polling as the correctness
	// path when no listener is connected. The payload contains no owner data.
	if _, err = db.Exec(ctx, `SELECT pg_notify('cutmy_jobs','')`); err != nil {
		return j, err
	}
	if key != "" {
		var existing string
		if err = db.QueryRow(ctx, `SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2`, owner, key).Scan(&existing); err != nil {
			return j, err
		}
		return readJob(ctx, db, existing, owner)
	}
	return j, nil
}

func jobItems(r ExportRequest) []JobItem {
	if r.OutputMode == "joined" {
		var duration int64
		for _, selected := range r.Ranges {
			duration += selected.EndMS - selected.StartMS
		}
		return []JobItem{{ID: newID("item"), Label: "Joined result", EndMS: duration, Status: "queued"}}
	}
	items := make([]JobItem, len(r.Ranges))
	for i, selected := range r.Ranges {
		items[i] = JobItem{ID: newID("item"), Label: selected.Label, StartMS: selected.StartMS, EndMS: selected.EndMS, Status: "queued"}
	}
	return items
}
