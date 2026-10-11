
CREATE TABLE IF NOT EXISTS sources (
 id text PRIMARY KEY, owner text NOT NULL, title text NOT NULL,
 duration_ms bigint NOT NULL, kind text NOT NULL, path text NOT NULL DEFAULT '',
 url text NOT NULL DEFAULT '', width integer NOT NULL DEFAULT 0, height integer NOT NULL DEFAULT 0,
 embed_url text, thumbnail_url text, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sources_owner_idx ON sources(owner);
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
