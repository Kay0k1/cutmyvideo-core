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
