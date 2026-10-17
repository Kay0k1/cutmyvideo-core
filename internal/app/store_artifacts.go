package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) AddArtifact(ctx context.Context, owner, job, path, token string, a Artifact) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	c := s.storageConfig()
	tag, err := tx.Exec(ctx, `WITH leased AS MATERIALIZED (
 SELECT id,lease_until FROM jobs WHERE id=$3 AND owner=$2 AND lease_token=$9 AND status='running' AND cancel_requested=false FOR UPDATE
 ) INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,expires_at)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,CASE WHEN $10::double precision>0 THEN clock_timestamp()+($10*interval '1 second') ELSE NULL END FROM leased WHERE lease_until>clock_timestamp() ON CONFLICT(id) DO NOTHING`, a.ID, owner, job, path, a.Filename, a.SizeBytes, a.ActualStartMS, a.ActualEndMS, token, c.ArtifactTTL.Seconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err = registerStorageFile(ctx, tx, path, owner, "artifact", a.ID, observedSize(path, a.SizeBytes)); err != nil {
		return err
	}
	if c.MaxStorageBytes > 0 {
		if a.SizeBytes < 0 || a.SizeBytes > c.MaxOutputBytes {
			return errStorageUnavailable
		}
		tag, err = tx.Exec(ctx, "UPDATE storage_reservations SET size_bytes=size_bytes-$3 WHERE id=$1 AND token=$2 AND size_bytes>=$3", jobReservationID(job, token), token, c.MaxOutputBytes)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ArtifactPath(ctx context.Context, id, owner string) (string, string, error) {
	var path, name string
	err := s.DB.QueryRow(ctx, `SELECT path,filename FROM artifacts WHERE id=$1 AND owner=$2`, id, owner).Scan(&path, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return path, name, err
}

// artifactDownloadMetadata retains the artifact owner's existing lookup policy.
// Classification belongs to the original immutable job request, not a project's
// current plan or the filename. Unknown/legacy metadata cannot enable the hint.
func (s *Store) artifactDownloadMetadata(ctx context.Context, id, owner string) (string, string, bool, error) {
	var path, name string
	var draft bool
	err := s.DB.QueryRow(ctx, `SELECT a.path,a.filename,
 COALESCE(j.request->>'rendition','')='draft'
 AND COALESCE(j.request->>'format','')='mp4'
 AND COALESCE(j.request->>'cut_mode','')='accurate'
 AND COALESCE(j.request->>'output_mode','') IN ('','separate','joined')
 FROM artifacts a LEFT JOIN jobs j ON j.id=a.job_id AND j.owner=a.owner
 WHERE a.id=$1 AND a.owner=$2`, id, owner).Scan(&path, &name, &draft)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return path, name, draft, err
}

func decorateArtifactExpiry(ctx context.Context, db dbExecutor, j *Job) error {
	var ids []string
	for _, item := range j.Items {
		if item.Artifact != nil {
			ids = append(ids, item.Artifact.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// dbExecutor intentionally exposes QueryRow for transaction fakes. A single
	// aggregated row returns all result deadlines and avoids N+1 polling queries.
	var raw []byte
	err := db.QueryRow(ctx, `SELECT COALESCE(jsonb_object_agg(a.id,CASE WHEN parent.status IN ('queued','running','waiting_storage') THEN NULL ELSE a.expires_at END),'{}'::jsonb) FROM artifacts a JOIN jobs parent ON parent.id=a.job_id WHERE a.id=ANY($1) AND a.owner=$2`, ids, j.Owner).Scan(&raw)
	if err != nil {
		return err
	}
	var deadlines map[string]*time.Time
	if err = json.Unmarshal(raw, &deadlines); err != nil {
		return err
	}
	for i := range j.Items {
		a := j.Items[i].Artifact
		if a == nil {
			continue
		}
		expiry, exists := deadlines[a.ID]
		if !exists {
			j.Items[i].Artifact = nil
			j.Items[i].Message = "Result expired"
		} else {
			a.ExpiresAt = expiry
		}
	}
	return nil
}
