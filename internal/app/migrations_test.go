package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migrationFixture struct {
	db             *pgxpool.Pool
	url, namespace string
}

func newMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set isolated TEST_DATABASE_URL to run PostgreSQL migration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	namespace := newID("migration_test")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{namespace}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", namespace)
	query.Set("pool_max_conns", "2")
	parsed.RawQuery = query.Encode()
	db, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{namespace}.Sanitize()+" CASCADE"); err != nil {
			t.Error("migration fixture cleanup failed", err)
		}
		admin.Close()
	})
	return migrationFixture{db: db, url: parsed.String(), namespace: namespace}
}

func migrationExec(t *testing.T, db *pgxpool.Pool, sql string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
}

func installHistoricalSchema(t *testing.T, f migrationFixture, fixture string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "migrations", fixture+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, f.db, canonicalMigrationSQL(data))
}

func openMigrationStore(t *testing.T, f migrationFixture) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := OpenStore(ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.DB.Close)
	return s
}

// Only tiny synthetic fixture data is snapshotted. Actual startup validates
// catalog metadata and does not scan application data or byte counters.
func migrationSnapshot(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	shape, err := readSchemaShape(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]json.RawMessage)
	for table := range shape.Tables {
		var data []byte
		if err = tx.QueryRow(ctx, "SELECT COALESCE(jsonb_agg(jsonb_build_object('row',to_jsonb(t),'xmin',t.xmin::text) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM "+pgx.Identifier{table}.Sanitize()+" t").Scan(&data); err != nil {
			t.Fatal(err)
		}
		rows[table] = data
	}
	var physical []byte
	if err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('oid',c.oid,'name',c.relname,'kind',c.relkind,'node',c.relfilenode,'xmin',c.xmin::text) ORDER BY c.relname),'[]'::jsonb) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()`).Scan(&physical); err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(struct {
		Shape    schemaShape
		Rows     map[string]json.RawMessage
		Physical json.RawMessage
	}{shape, rows, physical})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func assertMigrationLedger(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	if err = checkMigrationLedger(ctx, tx, len(schemaMigrations)); err != nil {
		t.Fatal(err)
	}
	var markers int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM app_schema_versions").Scan(&markers); err != nil || markers != len(schemaMigrations) {
		t.Fatal("migration marker count differs from ordered ledger", markers, err)
	}
}

func TestMigrationsFreshAndCurrentStartupsNeverRewriteSchema(t *testing.T) {
	f := newMigrationFixture(t)
	s := openMigrationStore(t, f)
	assertMigrationLedger(t, s)
	before := migrationSnapshot(t, f.db)
	// A routine startup must not take ALTER TABLE locks or rebuild objects.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lock, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	if _, err = lock.Exec(ctx, "LOCK TABLE sources,jobs,artifacts IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	reopened := openMigrationStore(t, f)
	assertMigrationLedger(t, reopened)
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if after := migrationSnapshot(t, f.db); after != before {
		t.Fatal("routine startup changed catalog objects, migration records or application rows")
	}
}

const historicalRows = `
INSERT INTO sources(id,owner,title,duration_ms,kind,path) VALUES('src_legacy','legacy_owner','Legacy source',10000,'upload','/fixture/source');
INSERT INTO jobs(id,owner,source_id,request,items,status,stage,idempotency_key) VALUES
 ('job_queued','legacy_owner','src_legacy','{"source_id":"src_legacy","ranges":[{"start_ms":1000,"end_ms":3000,"label":"Тест"}],"format":"mp4","quality":"best","cut_mode":"accurate"}',
 '[{"id":"item_legacy","label":"Тест","start_ms":1000,"end_ms":3000,"status":"queued","artifact":null}]','queued','queued','legacy-idempotency'),
 ('job_finished','legacy_owner','src_legacy','{}','[]','succeeded','finished',NULL);
INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms)
 VALUES('art_legacy','legacy_owner','job_finished','/fixture/artifact','legacy.mp4',200,1000,3000);
`

func historicalRowsSnapshot(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result string
	if err := db.QueryRow(ctx, `SELECT jsonb_build_object(
 'source',(SELECT jsonb_build_object('id',id,'owner',owner,'title',title,'duration',duration_ms,'path',path,'created',created_at) FROM sources WHERE id='src_legacy'),
 'jobs',(SELECT jsonb_agg(jsonb_build_object('id',id,'request',request,'items',items,'status',status,'stage',stage,'key',idempotency_key,'created',created_at,'updated',updated_at) ORDER BY id) FROM jobs),
 'artifact',(SELECT to_jsonb(a)-'expires_at' FROM artifacts a WHERE id='art_legacy'))::text`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMigrationsUpgradeActualV021KeepsQueueLedgerAndData(t *testing.T) {
	f := newMigrationFixture(t)
	installHistoricalSchema(t, f, "v0.2.1")
	migrationExec(t, f.db, historicalRows+`
INSERT INTO storage_files(path,owner,resource_id,kind,size_bytes) VALUES('/fixture/source','legacy_owner','src_legacy','source',100),('/fixture/artifact','legacy_owner','art_legacy','artifact',200);
INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,token,job_id) VALUES('legacy-reserve','legacy_owner','job',99,now()+interval '1 hour','legacy-token','job_queued');
INSERT INTO source_metadata_cache(source_id,owner,payload,expires_at) VALUES('src_legacy','legacy_owner',decode('010203','hex'),now()+interval '1 hour');`)
	before := historicalRowsSnapshot(t, f.db)
	s := openMigrationStore(t, f)
	assertMigrationLedger(t, s)
	if after := historicalRowsSnapshot(t, f.db); after != before {
		t.Fatal("upgrade changed historical source, queue, idempotency or completed artifact data")
	}
	var stored, reserved int64
	if err := s.DB.QueryRow(context.Background(), "SELECT stored_bytes,reserved_bytes FROM storage_counters WHERE id=1").Scan(&stored, &reserved); err != nil || stored != 300 || reserved != 99 {
		t.Fatal("upgrade changed ledger counters", stored, reserved, err)
	}
	var payload []byte
	if err := s.DB.QueryRow(context.Background(), "SELECT payload FROM source_metadata_cache WHERE source_id='src_legacy'").Scan(&payload); err != nil || !reflect.DeepEqual(payload, []byte{1, 2, 3}) {
		t.Fatal("upgrade changed cached metadata bytes", err)
	}
	job, err := s.CreateJobLimited(context.Background(), "legacy_owner", requestFor(Source{ID: "src_legacy"}), "legacy-idempotency", 4, 4)
	if err != nil || job.ID != "job_queued" {
		t.Fatal("historical idempotency replay no longer returns the existing queue entry", job.ID, err)
	}
	claimed, token, err := s.Claim(context.Background())
	if err != nil || claimed.ID != "job_queued" || token == "" {
		t.Fatal("upgraded queued job cannot be claimed", claimed.ID, err)
	}
	claimed.Status, claimed.Stage = "failed", "finished"
	if err = s.SaveJob(context.Background(), claimed, token); err != nil {
		t.Fatal("upgraded lease cannot save job progress", err)
	}
	migrationExec(t, f.db, "UPDATE storage_files SET size_bytes=105 WHERE path='/fixture/source'")
	if err = s.DB.QueryRow(context.Background(), "SELECT stored_bytes FROM storage_counters WHERE id=1").Scan(&stored); err != nil || stored != 305 {
		t.Fatal("upgraded accounting trigger stopped updating counters", stored, err)
	}
}

func TestMigrationsUpgradeRecognizedUnversionedHistory(t *testing.T) {
	for _, fixture := range []string{"unversioned-initial", "unversioned-provider-id", "unversioned-platform", "unversioned-metadata"} {
		t.Run(fixture, func(t *testing.T) {
			f := newMigrationFixture(t)
			installHistoricalSchema(t, f, fixture)
			migrationExec(t, f.db, historicalRows)
			before := historicalRowsSnapshot(t, f.db)
			s := openMigrationStore(t, f)
			assertMigrationLedger(t, s)
			if after := historicalRowsSnapshot(t, f.db); after != before {
				t.Fatal("legacy upgrade changed existing records")
			}
			if _, err := s.Source(context.Background(), "src_legacy", "legacy_owner"); err != nil {
				t.Fatal("upgraded legacy source is unreadable", err)
			}
			if job, _, err := s.Claim(context.Background()); err != nil || job.ID != "job_queued" {
				t.Fatal("legacy queue cannot resume", job.ID, err)
			}
		})
	}
}

func TestMigrationsRejectUnknownFutureOrCorruptSchemaWithoutChanges(t *testing.T) {
	for _, scenario := range []struct {
		name, alter string
		latest      bool
	}{
		{"unknown-marker", "INSERT INTO app_schema_versions(version) VALUES('unknown-private-fixture')", false},
		{"future-marker", "INSERT INTO app_schema_versions(version) VALUES('20991231-future-release')", false},
		{"only-future-marker", "DELETE FROM app_schema_versions; INSERT INTO app_schema_versions(version) VALUES('20991231-future-release')", false},
		{"missing-predecessor", "DELETE FROM app_schema_versions WHERE version='20261005-storage-queue-v3'", true},
		{"bad-checksum", "UPDATE app_schema_migrations SET checksum=repeat('0',64) WHERE position=1", true},
		{"missing-ledger-record", "DELETE FROM app_schema_migrations WHERE position=1", true},
		{"wrong-ordinal", "UPDATE app_schema_migrations SET position=3 WHERE position=2", true},
		{"missing-required-column", "ALTER TABLE jobs DROP COLUMN storage_wait_until", false},
		{"wrong-column-type", "ALTER TABLE storage_files ALTER COLUMN size_bytes TYPE integer", false},
		{"missing-accounting-trigger", "DROP TRIGGER storage_file_insert ON storage_files", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newMigrationFixture(t)
			if scenario.latest {
				_ = openMigrationStore(t, f)
			} else {
				installHistoricalSchema(t, f, "v0.2.1")
			}
			migrationExec(t, f.db, historicalRows)
			migrationExec(t, f.db, scenario.alter)
			before := migrationSnapshot(t, f.db)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := OpenStore(ctx, f.url)
			if s != nil || !errors.Is(err, ErrSchemaIncompatible) {
				if s != nil {
					s.DB.Close()
				}
				t.Fatal("incompatible schema was not refused", err)
			}
			if after := migrationSnapshot(t, f.db); after != before {
				t.Fatal("refused startup changed catalog, version records or application data")
			}
		})
	}
}

func TestMigrationsConcurrentStartupsApplyOneOrderedChain(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var group sync.WaitGroup
	errors := make(chan error, 6)
	for range 6 {
		group.Go(func() {
			s, err := OpenStore(ctx, f.url)
			if s != nil {
				s.DB.Close()
			}
			errors <- err
		})
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal("concurrent startup failed", err)
		}
	}
	s := openMigrationStore(t, f)
	assertMigrationLedger(t, s)
}

func TestMigrationsCancellationRollsBackPartialDDLAndRecovers(t *testing.T) {
	for _, stage := range []string{"migration-lock", "legacy-ddl-lock"} {
		t.Run(stage, func(t *testing.T) {
			f := newMigrationFixture(t)
			if stage == "legacy-ddl-lock" {
				installHistoricalSchema(t, f, "unversioned-initial")
			}
			before := migrationSnapshot(t, f.db)
			lock, err := f.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackStorage(lock)
			sql := "SELECT pg_advisory_xact_lock(hashtext('cutmy:migrations'))"
			if stage == "legacy-ddl-lock" {
				sql = "LOCK TABLE sources IN ACCESS SHARE MODE"
			}
			if _, err = lock.Exec(context.Background(), sql); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			started := time.Now()
			s, err := OpenStore(ctx, f.url)
			cancel()
			elapsed := time.Since(started)
			if s != nil || !errors.Is(err, context.DeadlineExceeded) || elapsed > time.Second {
				if s != nil {
					s.DB.Close()
				}
				t.Fatal("migration cancellation did not return promptly", elapsed, err)
			}
			t.Logf("%s elapsed=%s", stage, elapsed)
			if err = lock.Rollback(context.Background()); err != nil {
				t.Fatal(err)
			}
			if after := migrationSnapshot(t, f.db); after != before {
				t.Fatal("cancelled migration retained partial DDL or marker changes")
			}
			reopened := openMigrationStore(t, f)
			assertMigrationLedger(t, reopened)
		})
	}
}

func TestMigrationsIsolateTargetSchemaFromSearchPathFallback(t *testing.T) {
	neighbor := newMigrationFixture(t)
	installHistoricalSchema(t, neighbor, "v0.2.1")
	migrationExec(t, neighbor.db, historicalRows)
	before := migrationSnapshot(t, neighbor.db)
	target := newMigrationFixture(t)
	parsed, err := url.Parse(target.url)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", target.namespace+","+neighbor.namespace)
	parsed.RawQuery = query.Encode()
	target.url = parsed.String()
	s := openMigrationStore(t, target)
	assertMigrationLedger(t, s)
	var count int
	if err = s.DB.QueryRow(context.Background(), "SELECT count(*) FROM sources").Scan(&count); err != nil || count != 0 {
		t.Fatal("target startup reused another schema's application table", count, err)
	}
	if after := migrationSnapshot(t, neighbor.db); after != before {
		t.Fatal("target startup changed fallback schema's legacy data")
	}
}

func TestMigrationInputsArePinnedAndPrefixValidationIsConservative(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "migrations", "v0.2.1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(canonicalMigrationSQL(fixture), migrationSQL(schemaMigrations[0])) {
		t.Fatal("immutable baseline differs from captured released v0.2.1 SQL")
	}
	if schemaVersion != schemaMigrations[len(schemaMigrations)-1].version {
		t.Fatal("latest marker disagrees with migration order")
	}
	for _, versions := range [][]string{{"future"}, {schemaVersion}, {schemaVersion, schemaVersion}} {
		if _, err := knownMigrationPrefix(versions); !errors.Is(err, ErrSchemaIncompatible) {
			t.Fatal("unrecognized/non-prefix version history was accepted")
		}
	}
}

func TestMigrationSQLChecksumsArePinnedAcrossWindowsNewlines(t *testing.T) {
	// These independently pinned SHA-256 values define the immutable LF SQL
	// shipped with the first ordered migration ledger. The baseline is also
	// compared with the separately captured actual v0.2.1 fixture above.
	pinned := map[string]string{
		"20261005-storage-queue-v3":      "8630cebd9bf2bb2c038519d807f75456a60d8dea24cc1114304551270b3e949f",
		"20261010-ordered-migrations-v1": "17caf6f352044a62dd4bbfee154d02965d9c411ca6783422b96db5eed6b40e57",
	}
	for _, migration := range schemaMigrations {
		t.Run(migration.version, func(t *testing.T) {
			want, exists := pinned[migration.version]
			if !exists {
				t.Fatal("migration has no independently pinned immutable checksum")
			}
			if got := migrationChecksum(migration); got != want {
				t.Fatalf("shipped migration content changed: checksum=%s want=%s", got, want)
			}
			sql := migrationSQL(migration)
			windows := []byte(strings.ReplaceAll(sql, "\n", "\r\n"))
			canonical := canonicalMigrationSQL(windows)
			if canonical != sql {
				t.Fatal("Windows newlines changed migration execution text")
			}
			digest := sha256.Sum256([]byte(canonical))
			if got := hex.EncodeToString(digest[:]); got != want {
				t.Fatal("Windows newlines changed the immutable checksum", got)
			}
			changed := append(windows, []byte("\r\n-- content changed\r\n")...)
			digest = sha256.Sum256([]byte(canonicalMigrationSQL(changed)))
			if hex.EncodeToString(digest[:]) == want {
				t.Fatal("SQL content change passed the immutable checksum")
			}
		})
	}
}

func TestMigrationConstraintOrderIsIndependentOfDatabaseCollation(t *testing.T) {
	expected := latestSchemaShape()
	actual := latestSchemaShape()
	for name, table := range actual.Tables {
		slices.Reverse(table.Constraints)
		actual.Tables[name] = table
	}
	normalizeSchemaShape(actual)
	if err := checkSchemaShape(actual, expected, false); err != nil {
		t.Fatal("identical constraints in another catalog order were rejected", err)
	}
	// Normalization changes only order; incompatible definitions still fail.
	table := actual.Tables["jobs"]
	table.Constraints[0] += " changed"
	actual.Tables["jobs"] = table
	normalizeSchemaShape(actual)
	if err := checkSchemaShape(actual, expected, false); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatal("constraint content drift was accepted", err)
	}
}
