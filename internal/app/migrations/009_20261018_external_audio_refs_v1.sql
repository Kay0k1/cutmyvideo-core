-- Append-only external recording references. Legacy requests contain no
-- replacement reference: adding this nullable column rewrites no job/source
-- rows and does not reinterpret historical requests.
SELECT pg_advisory_xact_lock(736021912360105);
ALTER TABLE jobs ADD COLUMN audio_source_id text REFERENCES sources(id) ON DELETE SET NULL;
ALTER TABLE jobs ADD CONSTRAINT jobs_distinct_audio_source CHECK(audio_source_id IS NULL OR audio_source_id<>source_id);
CREATE INDEX jobs_audio_source_pin_idx ON jobs(audio_source_id,id) WHERE audio_source_id IS NOT NULL;

-- JSON remains the immutable job intent/provenance; relational membership is
-- derived on insertion or explicit request edits, including direct SQL DML.
-- FK SET NULL edits only the column, preserving completed jobs and artifacts
-- when their separate recording is explicitly removed.
CREATE FUNCTION derive_job_audio_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.audio_source_id := NULLIF(NEW.request#>>'{audio_replace,source_id}','');
 RETURN NEW;
END $$;
CREATE TRIGGER job_audio_source_derive BEFORE INSERT OR UPDATE OF request ON jobs FOR EACH ROW EXECUTE FUNCTION derive_job_audio_source();

-- A normal column-only edit must not bypass the derived membership. The FK's
-- delete action runs after the referenced source row has become invisible to
-- this transaction and may clear its now-missing reference.
CREATE FUNCTION guard_job_audio_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.request IS NOT DISTINCT FROM OLD.request AND NEW.audio_source_id IS DISTINCT FROM OLD.audio_source_id THEN
  IF NEW.audio_source_id IS NOT NULL OR EXISTS(SELECT 1 FROM sources WHERE id=OLD.audio_source_id) THEN
   RAISE EXCEPTION 'external audio membership must be changed through the job request' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_audio_source_guard BEFORE UPDATE OF audio_source_id ON jobs FOR EACH ROW EXECUTE FUNCTION guard_job_audio_source();

DROP TRIGGER job_pin_fence_update ON jobs;
CREATE TRIGGER job_pin_fence_update BEFORE UPDATE OF id,source_id,request,audio_source_id ON jobs FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();

CREATE OR REPLACE FUNCTION seed_source_pins() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.id IS NOT DISTINCT FROM OLD.id THEN RETURN NEW; END IF;
 END IF;
 SELECT count(*) INTO NEW.retention_job_pins FROM (
  SELECT id FROM jobs WHERE source_id=NEW.id
  UNION ALL SELECT id FROM jobs WHERE audio_source_id=NEW.id
 ) references_to_source;
 SELECT count(*) INTO NEW.retention_preview_pins FROM storage_reservations WHERE kind='preview' AND job_id=NEW.id;
 NEW.retention_pins_valid := true;
 IF TG_OP='INSERT' THEN NEW.retention_pin_revision := 0;
 ELSE NEW.retention_pin_revision := OLD.retention_pin_revision+1; END IF;
 RETURN NEW;
END $$;

-- Count the union of (job ID, recording ID) memberships. The distinct-source
-- constraint makes each job contribute at most one reference per recording.
-- Status/heartbeat changes have an empty symmetric difference and write no
-- source rows; ID-only edits still invalidate resumable child-audit cursors.
CREATE OR REPLACE FUNCTION update_source_job_pins() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE change record;
BEGIN
 IF TG_OP='INSERT' THEN
  FOR change IN SELECT source_id,count(*)::bigint AS delta FROM (
   SELECT source_id FROM pin_new
   UNION ALL SELECT audio_source_id FROM pin_new WHERE audio_source_id IS NOT NULL
  ) memberships GROUP BY source_id ORDER BY source_id LOOP
   UPDATE sources SET retention_job_pins=CASE WHEN retention_pins_valid THEN retention_job_pins+change.delta ELSE retention_job_pins END,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.source_id;
  END LOOP;
 ELSIF TG_OP='DELETE' THEN
  FOR change IN SELECT source_id,-count(*)::bigint AS delta FROM (
   SELECT source_id FROM pin_old
   UNION ALL SELECT audio_source_id FROM pin_old WHERE audio_source_id IS NOT NULL
  ) memberships GROUP BY source_id ORDER BY source_id LOOP
   UPDATE sources SET retention_job_pins=CASE WHEN retention_pins_valid AND retention_job_pins+change.delta>=0 THEN retention_job_pins+change.delta ELSE retention_job_pins END,
    retention_pins_valid=retention_pins_valid AND retention_job_pins+change.delta>=0,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.source_id;
  END LOOP;
 ELSE
  FOR change IN
   WITH new_memberships AS (
    SELECT id,source_id FROM pin_new
    UNION ALL SELECT id,audio_source_id FROM pin_new WHERE audio_source_id IS NOT NULL
   ), old_memberships AS (
    SELECT id,source_id FROM pin_old
    UNION ALL SELECT id,audio_source_id FROM pin_old WHERE audio_source_id IS NOT NULL
   )
   SELECT source_id,sum(delta)::bigint AS delta FROM (
    SELECT source_id,1::bigint AS delta FROM (SELECT * FROM new_memberships EXCEPT SELECT * FROM old_memberships) added
    UNION ALL
    SELECT source_id,-1::bigint AS delta FROM (SELECT * FROM old_memberships EXCEPT SELECT * FROM new_memberships) removed
   ) changes GROUP BY source_id ORDER BY source_id
  LOOP
   UPDATE sources SET retention_job_pins=CASE WHEN retention_pins_valid AND retention_job_pins+change.delta>=0 THEN retention_job_pins+change.delta ELSE retention_job_pins END,
    retention_pins_valid=retention_pins_valid AND retention_job_pins+change.delta>=0,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.source_id;
  END LOOP;
 END IF;
 RETURN NULL;
END $$;
