-- Captured from 91192c8dd26ac545515d93b50f63d50db121deba; do not regenerate from the current migration runner.
CREATE TABLE app_schema_versions(version text PRIMARY KEY);

CREATE TABLE IF NOT EXISTS sources (
 id text PRIMARY KEY, owner text NOT NULL, title text NOT NULL,
 duration_ms bigint NOT NULL, kind text NOT NULL, path text NOT NULL DEFAULT '',
 url text NOT NULL DEFAULT '', width integer NOT NULL DEFAULT 0, height integer NOT NULL DEFAULT 0,
 embed_url text, thumbnail_url text, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sources_owner_idx ON sources(owner);
ALTER TABLE sources ADD COLUMN IF NOT EXISTS provider_id text NOT NULL DEFAULT '';
ALTER TABLE sources ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT '';
ALTER TABLE sources ADD COLUMN IF NOT EXISTS thumbnail_path text NOT NULL DEFAULT '';
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
CREATE INDEX IF NOT EXISTS jobs_source_idx ON jobs(source_id);
CREATE TABLE IF NOT EXISTS artifacts (
 id text PRIMARY KEY, owner text NOT NULL, job_id text NOT NULL REFERENCES jobs(id),
 path text NOT NULL, filename text NOT NULL, size_bytes bigint NOT NULL,
 actual_start_ms bigint NOT NULL, actual_end_ms bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS artifacts_owner_idx ON artifacts(owner);
CREATE INDEX IF NOT EXISTS artifacts_job_idx ON artifacts(job_id);

CREATE TABLE IF NOT EXISTS source_metadata_cache (
 source_id text PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
 owner text NOT NULL, payload bytea NOT NULL CHECK (octet_length(payload)<=1048576),
 expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS source_metadata_cache_expiry_idx ON source_metadata_cache(expires_at);

CREATE TABLE IF NOT EXISTS storage_files (
 path text PRIMARY KEY, owner text NOT NULL DEFAULT '', resource_id text NOT NULL DEFAULT '',
 kind text NOT NULL, size_bytes bigint NOT NULL CHECK(size_bytes>=0),
 delete_pending boolean NOT NULL DEFAULT false, observed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS storage_files_owner_idx ON storage_files(owner,kind);
CREATE INDEX IF NOT EXISTS sources_owner_created_idx ON sources(owner,created_at DESC);
CREATE INDEX IF NOT EXISTS storage_files_pending_idx ON storage_files(path) WHERE delete_pending;
CREATE TABLE IF NOT EXISTS storage_reservations (
 id text PRIMARY KEY, owner text NOT NULL, kind text NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes>=0), expires_at timestamptz NOT NULL,
 token text NOT NULL DEFAULT '',job_id text NOT NULL DEFAULT '',abandoned_at timestamptz
);
ALTER TABLE storage_reservations ADD COLUMN IF NOT EXISTS job_id text NOT NULL DEFAULT '';
ALTER TABLE storage_reservations ADD COLUMN IF NOT EXISTS abandoned_at timestamptz;
CREATE INDEX IF NOT EXISTS storage_reservations_job_idx ON storage_reservations(job_id);
CREATE INDEX IF NOT EXISTS storage_reservations_expiry_idx ON storage_reservations(expires_at);
CREATE TABLE IF NOT EXISTS storage_counters (id integer PRIMARY KEY CHECK(id=1), stored_bytes numeric NOT NULL CHECK(stored_bytes>=0),reserved_bytes numeric NOT NULL CHECK(reserved_bytes>=0));
INSERT INTO storage_counters(id,stored_bytes,reserved_bytes)
 SELECT 1,COALESCE((SELECT sum(size_bytes) FROM storage_files),0),COALESCE((SELECT sum(size_bytes) FROM storage_reservations),0) ON CONFLICT(id) DO NOTHING;
CREATE OR REPLACE FUNCTION update_storage_counter() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE delta numeric; BEGIN
 IF TG_OP='INSERT' THEN SELECT COALESCE(sum(size_bytes),0) INTO delta FROM storage_new;
 ELSIF TG_OP='DELETE' THEN SELECT -COALESCE(sum(size_bytes),0) INTO delta FROM storage_old;
 ELSE SELECT COALESCE((SELECT sum(size_bytes) FROM storage_new),0)-COALESCE((SELECT sum(size_bytes) FROM storage_old),0) INTO delta; END IF;
 IF delta<>0 THEN
  IF TG_TABLE_NAME='storage_files' THEN UPDATE storage_counters SET stored_bytes=stored_bytes+delta WHERE id=1;
  ELSE UPDATE storage_counters SET reserved_bytes=reserved_bytes+delta WHERE id=1; END IF;
 END IF;
 RETURN NULL; END $$;
DROP TRIGGER IF EXISTS storage_file_counter ON storage_files;
DROP TRIGGER IF EXISTS storage_reservation_counter ON storage_reservations;
CREATE OR REPLACE TRIGGER storage_file_insert AFTER INSERT ON storage_files REFERENCING NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_file_update AFTER UPDATE ON storage_files REFERENCING OLD TABLE AS storage_old NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_file_delete AFTER DELETE ON storage_files REFERENCING OLD TABLE AS storage_old FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_reservation_insert AFTER INSERT ON storage_reservations REFERENCING NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_reservation_update AFTER UPDATE ON storage_reservations REFERENCING OLD TABLE AS storage_old NEW TABLE AS storage_new FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE OR REPLACE TRIGGER storage_reservation_delete AFTER DELETE ON storage_reservations REFERENCING OLD TABLE AS storage_old FOR EACH STATEMENT EXECUTE FUNCTION update_storage_counter();
CREATE TABLE IF NOT EXISTS storage_state (id text PRIMARY KEY);
CREATE TABLE IF NOT EXISTS queue_owners (owner text PRIMARY KEY,last_claimed timestamptz,last_considered timestamptz);
ALTER TABLE queue_owners ALTER COLUMN last_claimed DROP NOT NULL;
ALTER TABLE queue_owners ADD COLUMN IF NOT EXISTS last_considered timestamptz;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS storage_wait_until timestamptz;
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS expires_at timestamptz;

INSERT INTO app_schema_versions(version) VALUES('20261005-storage-queue-v3');
CREATE TABLE app_schema_migrations (
 position integer PRIMARY KEY CHECK(position>0),
 version text NOT NULL UNIQUE,
 checksum text NOT NULL CHECK(checksum ~ '^[0-9a-f]{64}$'),
 applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO app_schema_versions(version) VALUES('20261010-ordered-migrations-v1');
INSERT INTO app_schema_migrations(position,version,checksum) VALUES
 (1,'20261005-storage-queue-v3','8630cebd9bf2bb2c038519d807f75456a60d8dea24cc1114304551270b3e949f'),
 (2,'20261010-ordered-migrations-v1','17caf6f352044a62dd4bbfee154d02965d9c411ca6783422b96db5eed6b40e57');

ALTER TABLE storage_files
 ADD COLUMN delete_retry_at timestamptz NOT NULL DEFAULT '-infinity'::timestamptz,
 ADD COLUMN delete_failures integer NOT NULL DEFAULT 0 CHECK(delete_failures>=0);
CREATE INDEX storage_files_delete_due_idx ON storage_files(delete_retry_at,path) WHERE delete_pending;
DROP INDEX storage_files_pending_idx;
CREATE TABLE storage_scan_progress (
 data_dir text NOT NULL, scan_kind text NOT NULL, path text NOT NULL,
 position bigint NOT NULL DEFAULT 0 CHECK(position>=0),
 prefix_digest text NOT NULL DEFAULT '' CHECK(prefix_digest='' OR prefix_digest~'^[0-9a-f]{64}$'),
 generation_mtime_ns bigint, generation_size bigint,
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(), completed_at timestamptz,
 last_visited_at timestamptz NOT NULL DEFAULT '-infinity'::timestamptz,
 PRIMARY KEY(data_dir,scan_kind,path)
);
CREATE INDEX storage_scan_pending_idx ON storage_scan_progress(data_dir,scan_kind,last_visited_at,path) WHERE completed_at IS NULL;
CREATE TABLE storage_work_cleanup (
 data_dir text NOT NULL, path text NOT NULL,
 last_attempt_at timestamptz NOT NULL DEFAULT '-infinity'::timestamptz,
 PRIMARY KEY(data_dir,path)
);
CREATE INDEX storage_work_cleanup_attempt_idx ON storage_work_cleanup(data_dir,last_attempt_at,path);
CREATE INDEX artifacts_missing_expiry_idx ON artifacts(id) WHERE expires_at IS NULL;
CREATE INDEX sources_path_idx ON sources(path) WHERE path<>'';
CREATE INDEX sources_thumbnail_path_idx ON sources(thumbnail_path) WHERE thumbnail_path<>'';
CREATE INDEX artifacts_path_idx ON artifacts(path) WHERE path<>'';

CREATE INDEX artifacts_retention_expiry_idx ON artifacts(expires_at,created_at,id)
 WHERE expires_at IS NOT NULL;
CREATE INDEX artifacts_retention_legacy_idx ON artifacts(created_at,id)
 WHERE expires_at IS NULL;
CREATE INDEX jobs_retention_terminal_idx ON jobs(updated_at,id)
 WHERE status NOT IN ('queued','running','waiting_storage');
CREATE INDEX storage_files_orphan_retention_idx ON storage_files(observed_at,path)
 WHERE kind='orphan' AND NOT delete_pending;

INSERT INTO app_schema_versions(version) VALUES('20261010-storage-maintenance-v1');
INSERT INTO app_schema_migrations(position,version,checksum) VALUES(3,'20261010-storage-maintenance-v1','1c06b5c041a45201496d20655078515b5ca4b7896e2a43c3c848d51b2aa00627');
