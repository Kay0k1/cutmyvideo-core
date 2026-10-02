package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ DB *pgxpool.Pool }

type dbExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var ErrBusy = errors.New("too many active jobs")

func (s *Store) CreateJobLimited(ctx context.Context, owner string, r ExportRequest, key string, limit, globalLimit int) (Job, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cutmy:queue'))`); err != nil {
		return Job{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, owner); err != nil {
		return Job{}, err
	}
	if key != "" {
		var id string
		err = tx.QueryRow(ctx, `SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2`, owner, key).Scan(&id)
		if err == nil {
			return readJob(ctx, tx, id, owner)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Job{}, err
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE owner=$1 AND status IN ('queued','running')`, owner).Scan(&count); err != nil {
		return Job{}, err
	}
	if count >= limit {
		return Job{}, ErrBusy
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status IN ('queued','running')`).Scan(&count); err != nil {
		return Job{}, err
	}
	if count >= globalLimit {
		return Job{}, ErrBusy
	}
	j, err := insertJob(ctx, tx, owner, r, key)
	if err != nil {
		return j, err
	}
	err = tx.Commit(ctx)
	return j, err
}

func OpenStore(ctx context.Context, url string) (*Store, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	tx, e := db.Begin(ctx)
	if e != nil {
		db.Close()
		return nil, e
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cutmy:migrations'))`)
	if err == nil {
		_, err = tx.Exec(ctx, schema)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS sources (
 id text PRIMARY KEY, owner text NOT NULL, title text NOT NULL,
 duration_ms bigint NOT NULL, kind text NOT NULL, path text NOT NULL DEFAULT '',
 url text NOT NULL DEFAULT '', width integer NOT NULL DEFAULT 0, height integer NOT NULL DEFAULT 0,
 embed_url text, thumbnail_url text, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sources_owner_idx ON sources(owner);
ALTER TABLE sources ADD COLUMN IF NOT EXISTS provider_id text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS jobs (
 id text PRIMARY KEY, owner text NOT NULL, source_id text NOT NULL REFERENCES sources(id),
 request jsonb NOT NULL, items jsonb NOT NULL, status text NOT NULL DEFAULT 'queued',
 stage text NOT NULL DEFAULT 'queued', message text NOT NULL DEFAULT '',
 cancel_requested boolean NOT NULL DEFAULT false,
 lease_until timestamptz, lease_token text, attempts integer NOT NULL DEFAULT 0,
 idempotency_key text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner, idempotency_key)
);
CREATE INDEX IF NOT EXISTS jobs_claim_idx ON jobs(status, created_at);
CREATE TABLE IF NOT EXISTS artifacts (
 id text PRIMARY KEY, owner text NOT NULL, job_id text NOT NULL REFERENCES jobs(id),
 path text NOT NULL, filename text NOT NULL, size_bytes bigint NOT NULL,
 actual_start_ms bigint NOT NULL, actual_end_ms bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS artifacts_owner_idx ON artifacts(owner);
`

func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}

func (s *Store) AddSource(ctx context.Context, v Source) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,path,url,width,height,embed_url,thumbnail_url,provider_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, v.ID, v.Owner, v.Title, v.DurationMS, v.Kind, v.Path, v.URL, v.Width, v.Height, v.EmbedURL, v.ThumbnailURL, v.ProviderID)
	return err
}

func (s *Store) Source(ctx context.Context, id, owner string) (Source, error) {
	var v Source
	err := s.DB.QueryRow(ctx, `SELECT id,owner,title,duration_ms,kind,path,url,width,height,embed_url,thumbnail_url,provider_id FROM sources WHERE id=$1 AND owner=$2`, id, owner).Scan(&v.ID, &v.Owner, &v.Title, &v.DurationMS, &v.Kind, &v.Path, &v.URL, &v.Width, &v.Height, &v.EmbedURL, &v.ThumbnailURL, &v.ProviderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if v.Path != "" {
		u := "/api/v1/sources/" + v.ID + "/media"
		v.PreviewURL = &u
	}
	return v, err
}

func (s *Store) CreateJob(ctx context.Context, owner string, r ExportRequest, key string) (Job, error) {
	return insertJob(ctx, s.DB, owner, r, key)
}

func insertJob(ctx context.Context, db dbExecutor, owner string, r ExportRequest, key string) (Job, error) {
	j := Job{ID: newID("job"), Owner: owner, Status: "queued", Stage: "queued", Message: "Waiting for a worker", Request: r, Items: make([]JobItem, len(r.Ranges))}
	for i, v := range r.Ranges {
		j.Items[i] = JobItem{ID: newID("item"), Label: v.Label, StartMS: v.StartMS, EndMS: v.EndMS, Status: "queued"}
	}
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
	if key != "" {
		var existing string
		if err = db.QueryRow(ctx, `SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2`, owner, key).Scan(&existing); err != nil {
			return j, err
		}
		return readJob(ctx, db, existing, owner)
	}
	return j, nil
}

func (s *Store) Job(ctx context.Context, id, owner string) (Job, error) {
	return readJob(ctx, s.DB, id, owner)
}

func readJob(ctx context.Context, db dbExecutor, id, owner string) (Job, error) {
	var j Job
	var req, items []byte
	err := db.QueryRow(ctx, `SELECT id,owner,status,stage,message,request,items,cancel_requested FROM jobs WHERE id=$1 AND owner=$2`, id, owner).Scan(&j.ID, &j.Owner, &j.Status, &j.Stage, &j.Message, &req, &items, &j.Cancelled)
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
	return j, err
}

func (s *Store) Claim(ctx context.Context) (Job, string, error) {
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
	err := s.DB.QueryRow(ctx, `UPDATE jobs SET lease_until=now()+interval '45 seconds' WHERE id=$1 AND lease_token=$2 AND status='running' RETURNING cancel_requested`, id, token).Scan(&cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, ErrNotFound
	}
	return cancelled, err
}

func (s *Store) SaveJob(ctx context.Context, j Job, token string) error {
	items, _ := json.Marshal(j.Items)
	tag, err := s.DB.Exec(ctx, `UPDATE jobs SET status=CASE WHEN cancel_requested AND $3 IN ('succeeded','failed','cancelled') THEN 'cancelled' ELSE $3 END,stage=$4,message=CASE WHEN cancel_requested AND $3 IN ('succeeded','failed','cancelled') THEN 'Cancelled' ELSE $5 END,items=CASE WHEN cancel_requested AND $3 IN ('succeeded','failed','cancelled') THEN (SELECT jsonb_agg(CASE WHEN item->>'status' IN ('queued','running') THEN jsonb_set(item,'{status}','"cancelled"') ELSE item END) FROM jsonb_array_elements($6::jsonb) item) ELSE $6::jsonb END,updated_at=now() WHERE id=$1 AND lease_token=$2 AND status='running' AND lease_until>now()`, j.ID, token, j.Status, j.Stage, j.Message, items)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) Cancel(ctx context.Context, id, owner string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE jobs SET cancel_requested=true,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,message=CASE WHEN status='queued' THEN 'Cancelled' ELSE message END,items=CASE WHEN status='queued' THEN (SELECT jsonb_agg(CASE WHEN item->>'status' IN ('queued','running') THEN jsonb_set(item,'{status}','"cancelled"') ELSE item END) FROM jsonb_array_elements(items) item) ELSE items END,updated_at=now() WHERE id=$1 AND owner=$2 AND status IN ('queued','running')`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		_, err = s.Job(ctx, id, owner)
	}
	return err
}

func (s *Store) AddArtifact(ctx context.Context, owner, job, path, token string, a Artifact) error {
	tag, err := s.DB.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms) SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE EXISTS(SELECT 1 FROM jobs WHERE id=$3 AND owner=$2 AND lease_token=$9 AND lease_until>now() AND status='running' AND cancel_requested=false) ON CONFLICT(id) DO NOTHING`, a.ID, owner, job, path, a.Filename, a.SizeBytes, a.ActualStartMS, a.ActualEndMS, token)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) ArtifactPath(ctx context.Context, id, owner string) (string, string, error) {
	var path, name string
	err := s.DB.QueryRow(ctx, `SELECT path,filename FROM artifacts WHERE id=$1 AND owner=$2`, id, owner).Scan(&path, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return path, name, err
}

func (s *Store) Cleanup(ctx context.Context, artifactTTL, sourceTTL time.Duration) ([]string, error) {
	// Delete only completed resources: active jobs pin their source and results.
	rows, err := s.DB.Query(ctx, `DELETE FROM artifacts WHERE created_at<now()-($1 * interval '1 second') AND NOT EXISTS(SELECT 1 FROM jobs WHERE jobs.id=artifacts.job_id AND status IN ('queued','running')) RETURNING path`, artifactTTL.Seconds())
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM jobs WHERE status NOT IN ('queued','running') AND updated_at<now()-($1 * interval '1 second') AND NOT EXISTS(SELECT 1 FROM artifacts WHERE job_id=jobs.id)`, artifactTTL.Seconds())
	if err != nil {
		return paths, err
	}
	rows, err = s.DB.Query(ctx, `DELETE FROM sources WHERE created_at<now()-($1 * interval '1 second') AND NOT EXISTS(SELECT 1 FROM jobs WHERE source_id=sources.id) RETURNING path`, sourceTTL.Seconds())
	if err != nil {
		return paths, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			return paths, err
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, rows.Err()
}

func (s *Store) Recover(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `UPDATE jobs SET status=CASE WHEN cancel_requested THEN 'cancelled' ELSE 'failed' END,stage='finished',lease_token=NULL,message=CASE WHEN cancel_requested THEN 'Cancelled' ELSE 'Worker recovery limit reached' END,items=(SELECT jsonb_agg(CASE WHEN item->>'status' IN ('queued','running') THEN jsonb_set(item,'{status}',CASE WHEN cancel_requested THEN '"cancelled"'::jsonb ELSE '"failed"'::jsonb END) ELSE item END) FROM jsonb_array_elements(items) item) WHERE status='running' AND lease_until<now() AND (attempts>=3 OR cancel_requested)`)
	return err
}
