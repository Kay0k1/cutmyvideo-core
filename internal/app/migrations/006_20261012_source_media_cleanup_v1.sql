-- Additive source-media lifecycle. Jobs/results remain independent of media cleanup.
SELECT pg_advisory_xact_lock(736021912360105);
ALTER TABLE sources ADD COLUMN media_removed boolean NOT NULL DEFAULT false;
CREATE TABLE source_media_cleanup (
 source_id text PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
 successful_job_id text NOT NULL,
 protected_job_ids text[] NOT NULL,
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX source_media_cleanup_requested_idx ON source_media_cleanup(requested_at,source_id);
CREATE TABLE source_media_readers (
 id text PRIMARY KEY,
 source_id text NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
 expires_at timestamptz NOT NULL
);
CREATE INDEX source_media_readers_source_idx ON source_media_readers(source_id);
CREATE INDEX source_media_readers_expiry_idx ON source_media_readers(expires_at,id);
