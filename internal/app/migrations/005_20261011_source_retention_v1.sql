-- One atomic backfill: old writers use this fence; direct writers are also
-- excluded by table locks. The existing 30-second startup context caps the
-- lock wait, DDL, backfill and ledger commit together. A timeout rolls back all.
SELECT pg_advisory_xact_lock(736021912360105);
LOCK TABLE sources,jobs,storage_reservations IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE sources ADD COLUMN retention_job_pins bigint NOT NULL DEFAULT 0 CHECK(retention_job_pins>=0);
ALTER TABLE sources ADD COLUMN retention_preview_pins bigint NOT NULL DEFAULT 0 CHECK(retention_preview_pins>=0);
ALTER TABLE sources ADD COLUMN retention_pins_valid boolean NOT NULL DEFAULT true;
ALTER TABLE sources ADD COLUMN retention_pin_revision bigint NOT NULL DEFAULT 0 CHECK(retention_pin_revision>=0);
CREATE INDEX jobs_source_pin_idx ON jobs(source_id,id);
CREATE INDEX storage_reservations_preview_pin_idx ON storage_reservations(job_id,id) WHERE kind='preview';
UPDATE sources s SET retention_job_pins=COALESCE(j.pins,0),retention_preview_pins=COALESCE(p.pins,0)
FROM (SELECT source.id FROM sources source) all_sources
LEFT JOIN (SELECT source_id,count(*) AS pins FROM jobs GROUP BY source_id) j ON j.source_id=all_sources.id
LEFT JOIN (SELECT job_id,count(*) AS pins FROM storage_reservations WHERE kind='preview' GROUP BY job_id) p ON p.job_id=all_sources.id
WHERE s.id=all_sources.id;
CREATE INDEX sources_retention_eligible_idx ON sources(created_at,id)
 WHERE retention_pins_valid AND retention_job_pins=0 AND retention_preview_pins=0;
CREATE INDEX sources_retention_audit_idx ON sources(created_at,id);

-- Progress is application data, not a trusted eligibility assertion. Runtime
-- DML grants remain unchanged; final deletion always checks actual references.
CREATE TABLE source_retention_audit (
 id integer PRIMARY KEY CHECK(id=1),
 last_created_at timestamptz,last_source_id text,
 upper_created_at timestamptz,upper_source_id text,
 CHECK((last_created_at IS NULL)=(last_source_id IS NULL)),
 CHECK((upper_created_at IS NULL)=(upper_source_id IS NULL))
);
INSERT INTO source_retention_audit(id) VALUES(1);
CREATE TABLE source_pin_audit_progress (
 source_id text PRIMARY KEY REFERENCES sources(id) ON UPDATE CASCADE ON DELETE CASCADE,
 revision bigint NOT NULL CHECK(revision>=0),
 job_cursor text,job_count bigint NOT NULL DEFAULT 0 CHECK(job_count>=0),job_done boolean NOT NULL DEFAULT false,
 preview_cursor text,preview_count bigint NOT NULL DEFAULT 0 CHECK(preview_count>=0),preview_done boolean NOT NULL DEFAULT false
);

-- Invoker functions need no additional privileges beyond the current table DML
-- policy. In particular they are not SECURITY DEFINER functions with an ambient
-- search_path. Statement guards run before FK/row locks, including direct SQL.
CREATE FUNCTION acquire_source_pin_fence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(736021912360105);
 RETURN NULL;
END $$;
CREATE TRIGGER source_pin_fence_insert BEFORE INSERT ON sources FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER source_pin_fence_delete BEFORE DELETE ON sources FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER source_pin_fence_update BEFORE UPDATE OF id ON sources FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER job_pin_fence_insert BEFORE INSERT ON jobs FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER job_pin_fence_delete BEFORE DELETE ON jobs FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER job_pin_fence_update BEFORE UPDATE OF id,source_id ON jobs FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER preview_pin_fence_insert BEFORE INSERT ON storage_reservations FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER preview_pin_fence_delete BEFORE DELETE ON storage_reservations FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();
CREATE TRIGGER preview_pin_fence_update BEFORE UPDATE OF id,kind,job_id ON storage_reservations FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence();

-- Preview reservations deliberately have no source FK. Account for historical
-- reservations when a source is first published or an unreferenced ID moves.
CREATE FUNCTION seed_source_pins() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.id IS NOT DISTINCT FROM OLD.id THEN RETURN NEW; END IF;
 END IF;
 SELECT count(*) INTO NEW.retention_job_pins FROM jobs WHERE source_id=NEW.id;
 SELECT count(*) INTO NEW.retention_preview_pins FROM storage_reservations WHERE kind='preview' AND job_id=NEW.id;
 NEW.retention_pins_valid := true;
 IF TG_OP='INSERT' THEN NEW.retention_pin_revision := 0;
 ELSE NEW.retention_pin_revision := OLD.retention_pin_revision+1; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER source_pin_seed_insert BEFORE INSERT ON sources FOR EACH ROW EXECUTE FUNCTION seed_source_pins();
CREATE TRIGGER source_pin_seed_update BEFORE UPDATE OF id ON sources FOR EACH ROW EXECUTE FUNCTION seed_source_pins();

-- Membership, not status, is the pin. Transition tables contain only actual
-- inserted/deleted rows, so an idempotent INSERT adds no extra pin. An UPDATE
-- uses the symmetric difference of (id,source_id); heartbeats/status changes
-- neither rewrite source rows nor invalidate an in-progress audit.
CREATE FUNCTION update_source_job_pins() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE change record;
BEGIN
 IF TG_OP='INSERT' THEN
  FOR change IN SELECT source_id,count(*)::bigint AS delta FROM pin_new GROUP BY source_id ORDER BY source_id LOOP
   UPDATE sources SET retention_job_pins=CASE WHEN retention_pins_valid THEN retention_job_pins+change.delta ELSE retention_job_pins END,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.source_id;
  END LOOP;
 ELSIF TG_OP='DELETE' THEN
  FOR change IN SELECT source_id,-count(*)::bigint AS delta FROM pin_old GROUP BY source_id ORDER BY source_id LOOP
   UPDATE sources SET retention_job_pins=CASE WHEN retention_pins_valid AND retention_job_pins+change.delta>=0 THEN retention_job_pins+change.delta ELSE retention_job_pins END,
    retention_pins_valid=retention_pins_valid AND retention_job_pins+change.delta>=0,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.source_id;
  END LOOP;
 ELSE
  FOR change IN
   SELECT source_id,sum(delta)::bigint AS delta FROM (
    SELECT source_id,1::bigint AS delta FROM (SELECT id,source_id FROM pin_new EXCEPT SELECT id,source_id FROM pin_old) added
    UNION ALL
    SELECT source_id,-1::bigint AS delta FROM (SELECT id,source_id FROM pin_old EXCEPT SELECT id,source_id FROM pin_new) removed
   ) changes GROUP BY source_id ORDER BY source_id
  LOOP
   UPDATE sources SET retention_job_pins=CASE WHEN retention_pins_valid AND retention_job_pins+change.delta>=0 THEN retention_job_pins+change.delta ELSE retention_job_pins END,
    retention_pins_valid=retention_pins_valid AND retention_job_pins+change.delta>=0,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.source_id;
  END LOOP;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER source_job_pin_insert AFTER INSERT ON jobs REFERENCING NEW TABLE AS pin_new FOR EACH STATEMENT EXECUTE FUNCTION update_source_job_pins();
CREATE TRIGGER source_job_pin_delete AFTER DELETE ON jobs REFERENCING OLD TABLE AS pin_old FOR EACH STATEMENT EXECUTE FUNCTION update_source_job_pins();
CREATE TRIGGER source_job_pin_update AFTER UPDATE ON jobs REFERENCING OLD TABLE AS pin_old NEW TABLE AS pin_new FOR EACH STATEMENT EXECUTE FUNCTION update_source_job_pins();

-- Exact preview kind pins until its row is removed or changes membership.
-- Expiry, owner, token, abandoned_at, size and cached previews do not change it.
CREATE FUNCTION update_source_preview_pins() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE change record;
BEGIN
 IF TG_OP='INSERT' THEN
  FOR change IN SELECT job_id,count(*)::bigint AS delta FROM pin_new WHERE kind='preview' GROUP BY job_id ORDER BY job_id LOOP
   UPDATE sources SET retention_preview_pins=CASE WHEN retention_pins_valid THEN retention_preview_pins+change.delta ELSE retention_preview_pins END,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.job_id;
  END LOOP;
 ELSIF TG_OP='DELETE' THEN
  FOR change IN SELECT job_id,-count(*)::bigint AS delta FROM pin_old WHERE kind='preview' GROUP BY job_id ORDER BY job_id LOOP
   UPDATE sources SET retention_preview_pins=CASE WHEN retention_pins_valid AND retention_preview_pins+change.delta>=0 THEN retention_preview_pins+change.delta ELSE retention_preview_pins END,
    retention_pins_valid=retention_pins_valid AND retention_preview_pins+change.delta>=0,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.job_id;
  END LOOP;
 ELSE
  FOR change IN
   SELECT job_id,sum(delta)::bigint AS delta FROM (
    SELECT job_id,1::bigint AS delta FROM (SELECT id,job_id FROM pin_new WHERE kind='preview' EXCEPT SELECT id,job_id FROM pin_old WHERE kind='preview') added
    UNION ALL
    SELECT job_id,-1::bigint AS delta FROM (SELECT id,job_id FROM pin_old WHERE kind='preview' EXCEPT SELECT id,job_id FROM pin_new WHERE kind='preview') removed
   ) changes GROUP BY job_id ORDER BY job_id
  LOOP
   UPDATE sources SET retention_preview_pins=CASE WHEN retention_pins_valid AND retention_preview_pins+change.delta>=0 THEN retention_preview_pins+change.delta ELSE retention_preview_pins END,
    retention_pins_valid=retention_pins_valid AND retention_preview_pins+change.delta>=0,
    retention_pin_revision=retention_pin_revision+1 WHERE id=change.job_id;
  END LOOP;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER source_preview_pin_insert AFTER INSERT ON storage_reservations REFERENCING NEW TABLE AS pin_new FOR EACH STATEMENT EXECUTE FUNCTION update_source_preview_pins();
CREATE TRIGGER source_preview_pin_delete AFTER DELETE ON storage_reservations REFERENCING OLD TABLE AS pin_old FOR EACH STATEMENT EXECUTE FUNCTION update_source_preview_pins();
CREATE TRIGGER source_preview_pin_update AFTER UPDATE ON storage_reservations REFERENCING OLD TABLE AS pin_old NEW TABLE AS pin_new FOR EACH STATEMENT EXECUTE FUNCTION update_source_preview_pins();
