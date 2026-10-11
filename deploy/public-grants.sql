-- Run as cutmy_admin after the schema-owner's migration, with API/worker stopped.
-- Deliberately no runtime ownership, role membership, CREATE, TEMP or TRUNCATE.
BEGIN;
SET LOCAL search_path=pg_catalog;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM cutmy_runtime, PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM cutmy_runtime, PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM cutmy_runtime, PUBLIC;
DO $grants$
DECLARE item record;
BEGIN
 FOR item IN SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='public' AND c.relkind='r'
 LOOP
  IF item.relname IN ('app_schema_versions','app_schema_migrations') THEN
   -- Current startup takes SHARE locks. PostgreSQL 17 MAINTAIN permits these
   -- locks without allowing INSERT/UPDATE/DELETE on the migration history.
   EXECUTE format('GRANT SELECT, MAINTAIN ON TABLE public.%I TO cutmy_runtime', item.relname);
  ELSE
   EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.%I TO cutmy_runtime', item.relname);
  END IF;
 END LOOP;
END $grants$;
GRANT USAGE ON SCHEMA public TO cutmy_runtime;
GRANT CONNECT ON DATABASE cutmy TO cutmy_runtime;
-- Current schema has no sequences. Its only public function is the invoker
-- trigger update_storage_counter(): triggers check EXECUTE when created by the
-- schema owner; runtime DML does not need a blanket function EXECUTE grant.
COMMIT;
