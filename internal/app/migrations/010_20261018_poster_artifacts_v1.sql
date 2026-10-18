-- Image derivatives retain their own metadata and share their parent's lifetime.
-- No historical media/job row or completed request is rewritten.
SELECT pg_advisory_xact_lock(736021912360105);
CREATE TABLE image_artifacts (
 id text PRIMARY KEY,
 owner text NOT NULL,
 parent_artifact_id text NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
 path text NOT NULL UNIQUE,
 filename text NOT NULL,
 format text NOT NULL CHECK(format IN ('jpeg','png')),
 size_bytes bigint NOT NULL CHECK(size_bytes>0 AND size_bytes<=16777216),
 width integer NOT NULL CHECK(width>0),
 height integer NOT NULL CHECK(height>0 AND width::bigint*height<=16777216),
 timestamp_ms bigint NOT NULL CHECK(timestamp_ms>=0),
 expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(parent_artifact_id,timestamp_ms,format)
);
CREATE INDEX image_artifacts_owner_idx ON image_artifacts(owner,created_at);
CREATE INDEX image_artifacts_expiry_idx ON image_artifacts(expires_at,id);
CREATE TABLE artifact_readers (
 id text PRIMARY KEY,
 artifact_id text NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
 expires_at timestamptz NOT NULL
);
CREATE INDEX artifact_readers_parent_idx ON artifact_readers(artifact_id,expires_at);
CREATE INDEX artifact_readers_expiry_idx ON artifact_readers(expires_at,id);

-- Direct SQL deletion and FK cascades use the same publication/retention fence.
CREATE TRIGGER artifact_poster_delete_fence BEFORE DELETE ON artifacts FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER image_artifact_delete_fence BEFORE DELETE ON image_artifacts FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE FUNCTION tombstone_image_artifact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO storage_files(path,owner,kind,resource_id,size_bytes,delete_pending,delete_retry_at)
 VALUES(OLD.path,OLD.owner,'image_artifact',OLD.id,OLD.size_bytes,true,statement_timestamp())
 ON CONFLICT(path) DO UPDATE SET delete_pending=true,
 size_bytes=GREATEST(storage_files.size_bytes,excluded.size_bytes),
 delete_retry_at=CASE WHEN storage_files.delete_pending THEN storage_files.delete_retry_at ELSE excluded.delete_retry_at END,
 delete_failures=CASE WHEN storage_files.delete_pending THEN storage_files.delete_failures ELSE 0 END;
 RETURN OLD;
END $$;
CREATE TRIGGER image_artifact_tombstone AFTER DELETE ON image_artifacts FOR EACH ROW EXECUTE FUNCTION tombstone_image_artifact();
