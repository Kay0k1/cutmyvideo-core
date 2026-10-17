package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) Job(ctx context.Context, id, owner string) (Job, error) {
	return readJob(ctx, s.DB, id, owner)
}

// JobByIdempotencyKey recovers retained work without reapplying current admission
// limits. The caller must bound the database operation and validate the key.
func (s *Store) JobByIdempotencyKey(ctx context.Context, owner, key string) (Job, error) {
	if key == "" {
		return Job{}, ErrNotFound
	}
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2`, owner, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return readJob(ctx, s.DB, id, owner)
}

func readJob(ctx context.Context, db dbExecutor, id, owner string) (Job, error) {
	var j Job
	var req, items []byte
	err := db.QueryRow(ctx, `SELECT id,owner,status,stage,message,request,items,cancel_requested,storage_wait_until FROM jobs WHERE id=$1 AND owner=$2`, id, owner).Scan(&j.ID, &j.Owner, &j.Status, &j.Stage, &j.Message, &req, &items, &j.Cancelled, &j.StorageWaitUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if err = json.Unmarshal(req, &j.Request); err != nil {
		return j, err
	}
	err = json.Unmarshal(items, &j.Items)
	if err == nil {
		err = decorateArtifactExpiry(ctx, db, &j)
	}
	return j, err
}
