package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sourcePinExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
func sourcePinSource(t *testing.T, s *Store, id string) {
	t.Helper()
	sourcePinExec(t, s.DB, `INSERT INTO sources(id,owner,title,duration_ms,kind) VALUES($1,'owner','pin fixture',1000,'platform')`, id)
}
func assertSourcePins(t *testing.T, s *Store, id string, jobs, previews int64, valid bool) {
	t.Helper()
	var j, p, actualJ, actualP int64
	var v bool
	err := s.DB.QueryRow(context.Background(), `SELECT retention_job_pins,retention_preview_pins,retention_pins_valid,(SELECT count(*) FROM jobs WHERE source_id=s.id),(SELECT count(*) FROM storage_reservations WHERE kind='preview' AND job_id=s.id) FROM sources s WHERE id=$1`, id).Scan(&j, &p, &v, &actualJ, &actualP)
	if err != nil || j != jobs || p != previews || v != valid || (valid && (j != actualJ || p != actualP)) {
		t.Fatalf("source %s: stored=%d/%d/%t actual=%d/%d want=%d/%d/%t err=%v", id, j, p, v, actualJ, actualP, jobs, previews, valid, err)
	}
}
func sourcePinRevision(t *testing.T, s *Store, id string) (revision int64, xmin string) {
	t.Helper()
	if err := s.DB.QueryRow(context.Background(), `SELECT retention_pin_revision,xmin::text FROM sources WHERE id=$1`, id).Scan(&revision, &xmin); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSourcePinMembershipTransitionsAndRollback(t *testing.T) {
	s := testStore(t)
	for _, id := range []string{"first", "second"} {
		sourcePinSource(t, s, id)
	}
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items,idempotency_key) VALUES('job','owner','first','{}','[]','idem')`)
	assertSourcePins(t, s, "first", 1, 0, true)
	revision, xmin := sourcePinRevision(t, s, "first")
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items,idempotency_key) VALUES('duplicate','owner','first','{}','[]','idem') ON CONFLICT(owner,idempotency_key) DO NOTHING`)
	for _, status := range []string{"running", "waiting_storage", "succeeded", "failed", "cancelled"} {
		sourcePinExec(t, s.DB, `UPDATE jobs SET status=$1,lease_until=now(),updated_at=now() WHERE id='job'`, status)
		assertSourcePins(t, s, "first", 1, 0, true)
	}
	if r, x := sourcePinRevision(t, s, "first"); r != revision || x != xmin {
		t.Fatal("idempotency/status/heartbeat rewrote source pin state", r, x, revision, xmin)
	}
	sourcePinExec(t, s.DB, `UPDATE jobs SET source_id='second',id='renamed-job' WHERE id='job'`)
	assertSourcePins(t, s, "first", 0, 0, true)
	assertSourcePins(t, s, "second", 1, 0, true)
	revision, _ = sourcePinRevision(t, s, "second")
	sourcePinExec(t, s.DB, `UPDATE jobs SET id='temporary-job-id' WHERE id='renamed-job'; UPDATE jobs SET id='renamed-job' WHERE id='temporary-job-id'`)
	if r, _ := sourcePinRevision(t, s, "second"); r != revision+2 {
		t.Fatal("job ID-only change failed to invalidate child cursors")
	}
	// Expired and foreign-owner previews still pin; cached/source reservations do not.
	sourcePinExec(t, s.DB, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('preview','foreign','preview',7,now()-interval '1 day','first'),('cache','owner','preview_cache',3,now(),'first'),('source','owner','source',2,now(),'first')`)
	assertSourcePins(t, s, "first", 0, 1, true)
	revision, xmin = sourcePinRevision(t, s, "first")
	sourcePinExec(t, s.DB, `UPDATE storage_reservations SET owner='different',expires_at=now(),size_bytes=8,abandoned_at=now(),token='new' WHERE id='preview'`)
	if r, x := sourcePinRevision(t, s, "first"); r != revision || x != xmin {
		t.Fatal("non-membership preview update rewrote source pins")
	}
	tx, err := s.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `UPDATE storage_reservations SET kind='preview_cache' WHERE id='preview'; DELETE FROM jobs WHERE id='renamed-job'`); err != nil {
		rollbackStorage(tx)
		t.Fatal(err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSourcePins(t, s, "first", 0, 1, true)
	assertSourcePins(t, s, "second", 1, 0, true)
	sourcePinExec(t, s.DB, `UPDATE storage_reservations SET job_id='second',id='preview-moved' WHERE id='preview'`)
	assertSourcePins(t, s, "first", 0, 0, true)
	assertSourcePins(t, s, "second", 1, 1, true)
	revision, _ = sourcePinRevision(t, s, "second")
	sourcePinExec(t, s.DB, `UPDATE storage_reservations SET id='temporary-preview-id' WHERE id='preview-moved'; UPDATE storage_reservations SET id='preview-moved' WHERE id='temporary-preview-id'`)
	if r, _ := sourcePinRevision(t, s, "second"); r != revision+2 {
		t.Fatal("preview ID-only change failed to invalidate child cursors")
	}
	sourcePinExec(t, s.DB, `UPDATE storage_reservations SET kind='preview_cache' WHERE id='preview-moved'`)
	assertSourcePins(t, s, "second", 1, 0, true)
	sourcePinExec(t, s.DB, `UPDATE storage_reservations SET kind='preview' WHERE id='preview-moved'`)
	// Cancellation uses plain pool Exec outside storageTx in the actual preview path.
	sourcePinExec(t, s.DB, `DELETE FROM storage_reservations WHERE id='preview-moved'; DELETE FROM jobs WHERE id='renamed-job'`)
	assertSourcePins(t, s, "second", 0, 0, true)
	var reserved, actual int64
	if err = s.DB.QueryRow(context.Background(), `SELECT reserved_bytes,(SELECT sum(size_bytes) FROM storage_reservations) FROM storage_counters WHERE id=1`).Scan(&reserved, &actual); err != nil || reserved != actual {
		t.Fatal("pin triggers changed byte accounting", reserved, actual, err)
	}
}

func TestSourcePinBirthBulkAndIDMoveIncludesHistoricalPreview(t *testing.T) {
	s := testStore(t)
	sourcePinExec(t, s.DB, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('historic','other','preview',1,now()-interval '1 hour','birth'),('future-id','other','preview',2,now(),'moved')`)
	sourcePinSource(t, s, "birth")
	assertSourcePins(t, s, "birth", 0, 1, true)
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items) SELECT 'bulk-'||i,'owner','birth','{}','[]' FROM generate_series(1,9) i`)
	assertSourcePins(t, s, "birth", 9, 1, true)
	sourcePinExec(t, s.DB, `DELETE FROM jobs WHERE source_id='birth'; UPDATE sources SET id='moved' WHERE id='birth'`)
	assertSourcePins(t, s, "moved", 0, 1, true)
	sourcePinExec(t, s.DB, `DELETE FROM storage_reservations WHERE id='future-id'`)
	assertSourcePins(t, s, "moved", 0, 0, true)
}

func TestSourcePinFenceSerializesDirectPreviewWithDeletion(t *testing.T) {
	s := testStore(t)
	sourcePinSource(t, s, "raced")
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at=now()-interval '2 hours' WHERE id='raced'`)
	tx, err := s.storageTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(context.Background(), `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('raced-preview','other','preview',1,now(),'raced')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cleanup did not wait for preview transaction", err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch, err := s.cleanupStorageBatch(context.Background(), time.Hour, time.Hour)
	if err != nil || len(batch.paths) != 0 {
		t.Fatal("committed preview was lost in cleanup", batch, err)
	}
	assertSourcePins(t, s, "raced", 0, 1, true)
	// In the reverse order a direct membership writer must acquire the fence
	// before touching rows, then cancel without a partial pin/counter update.
	tx, err = s.storageTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = s.DB.Exec(ctx, `DELETE FROM storage_reservations WHERE id='raced-preview'`)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		rollbackStorage(tx)
		t.Fatal("direct cancellation skipped storage fence", err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSourcePins(t, s, "raced", 0, 1, true)
}

func TestSourceRetentionFalseZerosContinueToHealthyTail(t *testing.T) {
	s := testStore(t)
	sourcePinExec(t, s.DB, `INSERT INTO sources(id,owner,title,duration_ms,kind,created_at) SELECT 'pin-'||lpad(i::text,4,'0'),'owner','old pinned',1000,'platform',now()-interval '3 hours' FROM generate_series(1,200) i;
 INSERT INTO jobs(id,owner,source_id,request,items) SELECT 'job-'||id,'owner',id,'{}','[]' FROM sources;
 UPDATE sources SET retention_job_pins=0;
 INSERT INTO sources(id,owner,title,duration_ms,kind,created_at) VALUES('healthy-tail','owner','tail',1000,'platform',now()-interval '2 hours')`)
	first, err := s.cleanupStorageBatch(context.Background(), time.Hour, time.Hour)
	if err != nil || !first.full {
		t.Fatal("bad counter prefix did not request continuation", first, err)
	}
	var validZero int
	if err = s.DB.QueryRow(context.Background(), `SELECT count(*) FROM sources WHERE id LIKE 'pin-%' AND retention_pins_valid AND retention_job_pins=0`).Scan(&validZero); err != nil || validZero != 0 {
		t.Fatal("false zero remained eligible", validZero, err)
	}
	second, err := s.cleanupStorageBatch(context.Background(), time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var pins, tail int
	if err = s.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM sources WHERE id LIKE 'pin-%'),(SELECT count(*) FROM sources WHERE id='healthy-tail')`).Scan(&pins, &tail); err != nil || pins != 200 || tail != 0 {
		t.Fatal("healthy tail was hidden or real pin deleted", pins, tail, second, err)
	}
}

func TestSourcePinAuditPartialRestartRevisionAndFairness(t *testing.T) {
	s := testStore(t)
	for _, id := range []string{"a-hot", "b-free", "c-small"} {
		sourcePinSource(t, s, id)
	}
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at='2020-01-01'; INSERT INTO jobs(id,owner,source_id,request,items) SELECT lpad(i::text,4,'0'),'owner','a-hot','{}','[]' FROM generate_series(1,257) i;
 INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) SELECT 'p-'||lpad(i::text,4,'0'),'owner','preview',0,now(),'a-hot' FROM generate_series(1,257) i;
 UPDATE sources SET retention_job_pins=900,retention_preview_pins=900 WHERE id IN ('a-hot','b-free')`)
	unit := func() bool {
		t.Helper()
		done, _, err := s.auditSourcePinUnit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return done
	}
	if unit() {
		t.Fatal("hot partial source incorrectly completed sweep")
	}
	var count int64
	var cursor string
	if err := s.DB.QueryRow(context.Background(), `SELECT job_count,job_cursor FROM source_pin_audit_progress WHERE source_id='a-hot'`).Scan(&count, &cursor); err != nil || count != 128 || cursor != "0128" {
		t.Fatal("unbounded or missing child checkpoint", count, cursor, err)
	}
	var previewCount int64
	if err := s.DB.QueryRow(context.Background(), `SELECT preview_count FROM source_pin_audit_progress WHERE source_id='a-hot'`).Scan(&previewCount); err != nil || previewCount != 128 {
		t.Fatal("preview audit exceeded or lost bounded progress", previewCount, err)
	}
	unit()
	assertSourcePins(t, s, "b-free", 0, 0, true)
	// Restart performs only shape/history reads and retains the partial audit.
	// Open a second pool using the first pool's immutable connection config.
	reopened, err := OpenStore(context.Background(), s.DB.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	if err = reopened.DB.QueryRow(context.Background(), `SELECT job_count FROM source_pin_audit_progress WHERE source_id='a-hot'`).Scan(&count); err != nil || count != 128 {
		t.Fatal("restart discarded audit checkpoint", count, err)
	}
	unit()
	if !unit() {
		t.Fatal("sweep did not wrap after highwater")
	}
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items) VALUES('','owner','a-hot','{}','[]')`)
	unit()
	if err = s.DB.QueryRow(context.Background(), `SELECT job_count,job_cursor FROM source_pin_audit_progress WHERE source_id='a-hot'`).Scan(&count, &cursor); err != nil || count != 128 || cursor != "0127" {
		t.Fatal("revision change reused stale partial count or skipped empty ID", count, cursor, err)
	}
	// A rollback cannot advance either global or per-source progress.
	before := sourcePinAuditSnapshot(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = s.auditSourcePinUnit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled audit succeeded", err)
	}
	if sourcePinAuditSnapshot(t, s) != before {
		t.Fatal("cancelled audit changed progress")
	}
	// Finish three child chunks across full sweeps; hot work never hides b/c.
	for range 11 {
		unit()
	}
	assertSourcePins(t, s, "a-hot", 258, 257, true)
	var partial int
	if err = s.DB.QueryRow(context.Background(), `SELECT count(*) FROM source_pin_audit_progress WHERE source_id='a-hot'`).Scan(&partial); err != nil || partial != 0 {
		t.Fatal("completed child audit retained partial state", partial, err)
	}
	// Backdated arrivals behind the cursor must become visible on the next wrap.
	sourcePinSource(t, s, "backdated")
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at='2019-01-01',retention_preview_pins=5 WHERE id='backdated'`)
	for range 7 {
		unit()
	}
	assertSourcePins(t, s, "backdated", 0, 0, true)
}

func sourcePinAuditSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var value string
	if err := s.DB.QueryRow(context.Background(), `SELECT jsonb_build_object('cursor',(SELECT to_jsonb(a) FROM source_retention_audit a WHERE id=1),'partial',(SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY source_id),'[]'::jsonb) FROM source_pin_audit_progress p))::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSourcePinUnderflowQuarantinesUntilAuthoritativeAudit(t *testing.T) {
	s := testStore(t)
	sourcePinSource(t, s, "underflow")
	sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items) VALUES('underflow-job','owner','underflow','{}','[]'); UPDATE sources SET retention_job_pins=0 WHERE id='underflow'; DELETE FROM jobs WHERE id='underflow-job'`)
	assertSourcePins(t, s, "underflow", 0, 0, false)
	if _, _, err := s.auditSourcePinUnit(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSourcePins(t, s, "underflow", 0, 0, true)
}

func TestSourceRetentionLostCommitAcknowledgementKeepsTombstoneCharge(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	path := writeLedgerFile(t, c, "sources", "lost-ack", 9)
	if err := s.AddSource(context.Background(), Source{ID: "lost-ack", Owner: "owner", Title: "fixture", Kind: "upload", Path: path, DurationMS: 1000}); err != nil {
		t.Fatal(err)
	}
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at=now()-interval '2 hours' WHERE id='lost-ack'`)
	stored, reserved := ledgerBytes(t, s)
	batch, err := s.cleanupStorageBatchCommit(context.Background(), time.Hour, time.Hour, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(batch.paths) != 0 {
		t.Fatal("unknown commit was reported as an acknowledged batch", batch, err)
	}
	var exists, pending bool
	if err = s.DB.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM sources WHERE id='lost-ack'),EXISTS(SELECT 1 FROM storage_files WHERE path=$1 AND delete_pending)`, path).Scan(&exists, &pending); err != nil || exists || !pending {
		t.Fatal("committed metadata/tombstone missing", exists, pending, err)
	}
	if a, b := ledgerBytes(t, s); a != stored || b != reserved {
		t.Fatal("unknown acknowledgement released physical charge", a, b, stored, reserved)
	}
	retry, err := s.cleanupStorageBatch(context.Background(), time.Hour, time.Hour)
	if err != nil || len(retry.paths) != 1 || retry.paths[0] != path {
		t.Fatal("retry did not recover authoritative tombstone", retry, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("metadata commit removed physical file", err)
	}
	if err = s.DrainStorageDeletes(context.Background(), c, retry.paths); err != nil {
		t.Fatal(err)
	}
	if a, b := ledgerBytes(t, s); a != 0 || b != 0 {
		t.Fatal("drain double charged or retained tombstone", a, b)
	}
}

// Install the checked immutable prefix, rather than fabricating a version-only
// schema. Existing fixture files stay untouched as historical release inputs.
func installSourcePinPredecessor(t *testing.T, f migrationFixture) {
	t.Helper()
	migrationExec(t, f.db, `CREATE TABLE app_schema_versions(version text PRIMARY KEY)`)
	for i, m := range schemaMigrations[:4] {
		migrationExec(t, f.db, migrationSQL(m))
		sourcePinExec(t, f.db, `INSERT INTO app_schema_versions(version) VALUES($1)`, m.version)
		if i >= 1 {
			start := i
			if i == 1 {
				start = 0
			}
			for j := start; j <= i; j++ {
				sourcePinExec(t, f.db, `INSERT INTO app_schema_migrations(position,version,checksum) VALUES($1,$2,$3)`, j+1, schemaMigrations[j].version, migrationChecksum(schemaMigrations[j]))
			}
		}
	}
}

func TestSourcePinMigrationBackfillHistoryCancellationAndRestart(t *testing.T) {
	f := newMigrationFixture(t)
	installSourcePinPredecessor(t, f)
	migrationExec(t, f.db, historicalRows+`INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('old-preview','other','preview',7,now()-interval '1 day','src_legacy'),('old-cache','other','preview_cache',3,now(),'src_legacy')`)
	beforeRows := historicalRowsSnapshot(t, f.db)
	before := migrationSnapshot(t, f.db)
	// An old direct writer is excluded by the migration's table lock even if it
	// does not acquire the storage fence. The partial migration must roll back.
	lock, err := f.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	if _, err = lock.Exec(context.Background(), `LOCK TABLE storage_reservations IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	store, err := OpenStore(ctx, f.url)
	cancel()
	if store != nil || !errors.Is(err, context.DeadlineExceeded) {
		if store != nil {
			store.DB.Close()
		}
		t.Fatal("005 writer lock did not obey cancellation", err)
	}
	if err = lock.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if migrationSnapshot(t, f.db) != before {
		t.Fatal("cancelled 005 left DDL/data/history changes")
	}
	s := openMigrationStore(t, f)
	assertMigrationLedger(t, s)
	assertSourcePins(t, s, "src_legacy", 2, 1, true)
	if historicalRowsSnapshot(t, f.db) != beforeRows {
		t.Fatal("005 changed original source/job/artifact data")
	}
	var reserved int64
	if err = s.DB.QueryRow(context.Background(), `SELECT reserved_bytes FROM storage_counters WHERE id=1`).Scan(&reserved); err != nil || reserved != 10 {
		t.Fatal("backfill changed byte reservations", reserved, err)
	}
	before = migrationSnapshot(t, f.db)
	second := openMigrationStore(t, f)
	assertMigrationLedger(t, second)
	if migrationSnapshot(t, f.db) != before {
		t.Fatal("current startup repeated backfill or changed progress")
	}
	// Successful 005 is a schema transition, not the old same-schema profile.
	if _, err = knownMigrationPrefix([]string{schemaVersion}); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatal("005 accepted missing history")
	}
}

func TestSourcePinCatalogRejectsBodyPrivilegeColumnAndForeignTargetDrift(t *testing.T) {
	for _, scenario := range []struct{ name, sql string }{
		{"body", `CREATE OR REPLACE FUNCTION update_source_job_pins() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`},
		{"definer", `ALTER FUNCTION update_source_preview_pins() SECURITY DEFINER`},
		{"search-path", `ALTER FUNCTION acquire_source_pin_fence() SET search_path=public`},
		{"columns", `DROP TRIGGER job_pin_fence_update ON jobs; CREATE TRIGGER job_pin_fence_update BEFORE UPDATE OF id ON jobs FOR EACH STATEMENT EXECUTE FUNCTION acquire_source_pin_fence()`},
		{"condition", `DROP TRIGGER source_pin_seed_update ON sources; CREATE TRIGGER source_pin_seed_update BEFORE UPDATE OF id ON sources FOR EACH ROW WHEN (OLD.id<>NEW.id) EXECUTE FUNCTION seed_source_pins()`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newMigrationFixture(t)
			_ = openMigrationStore(t, f)
			migrationExec(t, f.db, scenario.sql)
			before := migrationSnapshot(t, f.db)
			refused, err := OpenStore(context.Background(), f.url)
			if refused != nil || !errors.Is(err, ErrSchemaIncompatible) {
				if refused != nil {
					refused.DB.Close()
				}
				t.Fatal("changed pin contract was accepted", err)
			}
			if migrationSnapshot(t, f.db) != before {
				t.Fatal("refusal changed schema/data")
			}
		})
	}
	t.Run("foreign-target", func(t *testing.T) {
		foreign := newMigrationFixture(t)
		_ = openMigrationStore(t, foreign)
		target := newMigrationFixture(t)
		_ = openMigrationStore(t, target)
		migrationExec(t, target.db, `DROP TRIGGER job_pin_fence_insert ON jobs; CREATE TRIGGER job_pin_fence_insert BEFORE INSERT ON jobs FOR EACH STATEMENT EXECUTE FUNCTION `+pgx.Identifier{foreign.namespace, "acquire_source_pin_fence"}.Sanitize()+`()`)
		before := migrationSnapshot(t, target.db)
		refused, err := OpenStore(context.Background(), target.url)
		if refused != nil || !errors.Is(err, ErrSchemaIncompatible) {
			if refused != nil {
				refused.DB.Close()
			}
			t.Fatal("foreign function target was normalized as owned", err)
		}
		if migrationSnapshot(t, target.db) != before {
			t.Fatal("foreign target refusal changed data")
		}
	})
}

func TestSourcePinInvokerTriggersWorkWithoutRuntimeFunctionExecute(t *testing.T) {
	f := newMigrationFixture(t)
	s := openMigrationStore(t, f)
	var canCreate bool
	if err := s.DB.QueryRow(context.Background(), `SELECT rolsuper OR rolcreaterole FROM pg_roles WHERE rolname=current_user`).Scan(&canCreate); err != nil {
		t.Fatal(err)
	}
	if !canCreate {
		t.Skip("isolated runtime privilege fixture requires CREATEROLE")
	}
	role := newID("pin_runtime")
	ident := pgx.Identifier{role}.Sanitize()
	namespace := pgx.Identifier{f.namespace}.Sanitize()
	migrationExec(t, f.db, `CREATE ROLE `+ident+` NOLOGIN`)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.db.Exec(ctx, `DROP OWNED BY `+ident+`; DROP ROLE `+ident); err != nil {
			t.Error("runtime role fixture cleanup", err)
		}
	})
	migrationExec(t, f.db, `GRANT USAGE ON SCHEMA `+namespace+` TO `+ident+`; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA `+namespace+` TO `+ident+`; REVOKE ALL ON ALL FUNCTIONS IN SCHEMA `+namespace+` FROM PUBLIC,`+ident+`; REVOKE INSERT,UPDATE,DELETE ON app_schema_versions,app_schema_migrations FROM `+ident)
	sourcePinSource(t, s, "runtime")
	tx, err := s.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(context.Background(), `SET LOCAL ROLE `+ident); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `INSERT INTO jobs(id,owner,source_id,request,items) VALUES('runtime-job','owner','runtime','{}','[]'); INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('runtime-preview','owner','preview',5,now(),'runtime')`); err != nil {
		t.Fatal("invoker DML required extra function privileges", err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSourcePins(t, s, "runtime", 1, 1, true)
	var definer, execute bool
	if err = s.DB.QueryRow(context.Background(), `SELECT bool_or(p.prosecdef),bool_or(has_function_privilege($1,p.oid,'EXECUTE')) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$2`, role, f.namespace).Scan(&definer, &execute); err != nil || definer || execute {
		t.Fatal("pin functions expanded runtime privilege", definer, execute, err)
	}
	// Existing table grants allow derived-counter edits; the authoritative guard
	// therefore remains necessary under the actual runtime privilege model.
	tx, err = s.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `SET LOCAL ROLE `+ident+`; UPDATE sources SET retention_job_pins=0,retention_preview_pins=0 WHERE id='runtime'`); err != nil {
		rollbackStorage(tx)
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at=now()-interval '2 hours' WHERE id='runtime'`)
	if _, err = s.cleanupStorageBatch(context.Background(), time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	assertSourcePins(t, s, "runtime", 1, 1, true)
}

func TestSourcePinCatalogManifestsDeclareExactManagedFunctionsAndTriggerColumns(t *testing.T) {
	// A pure conservative-shape control complements real catalog observations.
	expected := latestSchemaShape()
	if len(expected.Functions) != 5 {
		t.Fatal("managed pin functions missing from shape")
	}
	for _, m := range schemaMigrations[:4] {
		old := expectedSchemaShape(m.shape)
		if _, exists := old.Tables["source_pin_audit_progress"]; exists {
			t.Fatal("old manifest reinterpreted as 005")
		}
	}
	for _, change := range []func(schemaShape){
		func(s schemaShape) { s.Functions["update_source_job_pins"] += "changed" },
		func(s schemaShape) {
			x := s.Tables["jobs"].Triggers["job_pin_fence_update"]
			x.Columns = []string{"id"}
			s.Tables["jobs"].Triggers["job_pin_fence_update"] = x
		},
		func(s schemaShape) {
			x := s.Tables["sources"].Triggers["source_pin_seed_update"]
			x.When = "old.id <> new.id"
			s.Tables["sources"].Triggers["source_pin_seed_update"] = x
		},
		func(s schemaShape) {
			x := s.Tables["jobs"].Triggers["job_pin_fence_insert"]
			x.Function = "foreign.acquire_source_pin_fence"
			s.Tables["jobs"].Triggers["job_pin_fence_insert"] = x
		},
	} {
		actual := latestSchemaShape()
		change(actual)
		if err := checkSchemaShape(actual, expected, false); !errors.Is(err, ErrSchemaIncompatible) {
			t.Fatal("changed pin catalog was accepted", err)
		}
	}
	if err := checkSchemaShape(expected, expectedSchemaShape(schemaMigrations[3].shape), false); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatal("005 shape was accepted as 004")
	}
	// Source runtime changed intentionally. The sealed A1 public profile must
	// refuse this candidate until a separately reviewed 005 transition is added.
	driver, err := os.ReadFile(filepath.Join("..", "..", "scripts", "self-hosting-acceptance.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(driver), `runtime_application_sources_unchanged`) || !strings.Contains(string(driver), `app(self.old_source) == app(self.current_source)`) {
		t.Fatal("old same-schema acceptance no longer refuses changed runtime inputs")
	}
}
