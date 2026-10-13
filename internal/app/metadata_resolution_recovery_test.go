package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func metadataResolutionRecoveryRows(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	shape, err := readSchemaShape(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]json.RawMessage{}
	for table := range shape.Tables {
		var data []byte
		if err := tx.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&data); err != nil {
			t.Fatal(err)
		}
		rows[table] = data
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMetadataResolutionPreviousBinaryRefuses007WithoutWrites(t *testing.T) {
	previous := os.Getenv("CUTMY_TEST_PREVIOUS_CORE_BINARY")
	if previous == "" {
		t.Skip("set exact owned previous006 binary for compatibility refusal evidence")
	}
	if !filepath.IsAbs(previous) {
		t.Fatal("previous binary must have an absolute owned path")
	}
	f := newMigrationFixture(t)
	s := openMigrationStore(t, f)
	source, info := storedMetadataSource(t, s)
	if err := s.CachePlatformMetadata(context.Background(), source, info); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.claimPlatformMetadataResolution(context.Background(), source, "retained-token"); err != nil || !claimed {
		t.Fatal("fixture resolution claim failed")
	}
	before := migrationSnapshot(t, s.DB)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, previous, "worker")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DATABASE_URL=" + f.url, "DATA_DIR=" + t.TempDir(), "GOMAXPROCS=2", "GOMEMLIMIT=128MiB"}
	output, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !bytes.Contains(output, []byte("database schema is incompatible")) {
		t.Fatal("previous binary did not refuse007 before running; private command output intentionally withheld")
	}
	if after := migrationSnapshot(t, s.DB); after != before {
		t.Fatal("previous binary refusal mutated007 catalog/history/rows")
	}
	_ = openMigrationStore(t, f)
	if after := migrationSnapshot(t, s.DB); after != before {
		t.Fatal("compatible007 reopen mutated persisted claims or other state")
	}
}

func TestMetadataResolutionRuntimeGrantsPermitLeaseCRUDWithoutOwnership(t *testing.T) {
	f := newMigrationFixture(t)
	admin := openMigrationStore(t, f)
	source, info := storedMetadataSource(t, admin)
	role, password := newID("metadata_runtime"), newID("private")
	roleSQL := pgx.Identifier{role}.Sanitize()
	// Both values are task-generated identifiers, never product configuration.
	migrationExec(t, f.db, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+password+"'")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = f.db.Exec(ctx, "DROP OWNED BY "+roleSQL)
		if _, err := f.db.Exec(ctx, "DROP ROLE "+roleSQL); err != nil {
			t.Error("owned runtime role cleanup failed")
		}
	})
	namespace := pgx.Identifier{f.namespace}.Sanitize()
	migrationExec(t, f.db, "GRANT USAGE ON SCHEMA "+namespace+" TO "+roleSQL)
	var databaseName string
	if err := f.db.QueryRow(context.Background(), `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal("owned database identity unavailable")
	}
	migrationExec(t, f.db, "GRANT CONNECT ON DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" TO "+roleSQL)
	for table := range latestSchemaShape().Tables {
		privileges := "SELECT, INSERT, UPDATE, DELETE"
		if table == "app_schema_versions" || table == "app_schema_migrations" {
			privileges = "SELECT, MAINTAIN"
		}
		migrationExec(t, f.db, "GRANT "+privileges+" ON TABLE "+namespace+"."+pgx.Identifier{table}.Sanitize()+" TO "+roleSQL)
	}
	parsed, err := url.Parse(f.url)
	if err != nil {
		t.Fatal("owned fixture URL parsing failed")
	}
	parsed.User = url.UserPassword(role, password)
	runtime, err := OpenStore(context.Background(), parsed.String())
	if err != nil {
		t.Fatal("runtime grant startup failed; private connection details withheld")
	}
	defer runtime.DB.Close()
	var owns, canCreate bool
	if err := runtime.DB.QueryRow(context.Background(), `SELECT n.nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user),has_schema_privilege(current_schema(),'CREATE') FROM pg_namespace n WHERE n.nspname=current_schema()`).Scan(&owns, &canCreate); err != nil || owns || canCreate {
		t.Fatal("runtime unexpectedly owns or can create in application schema")
	}
	claimed, err := runtime.claimPlatformMetadataResolution(context.Background(), source, "runtime-token")
	if err != nil || !claimed {
		t.Fatal("runtime INSERT/SELECT lease claim failed")
	}
	if _, err := runtime.DB.Exec(context.Background(), `UPDATE source_metadata_resolutions SET expires_at=clock_timestamp()+interval '1 minute' WHERE source_id=$1 AND token=$2`, source.ID, "runtime-token"); err != nil {
		t.Fatal("runtime UPDATE lease failed")
	}
	if err := runtime.cacheResolvedPlatformMetadata(context.Background(), source, "runtime-token", info); err != nil {
		t.Fatal("runtime cache publication and lease DELETE failed")
	}
	if _, hit := readPlatformMetadataCache(context.Background(), runtime.DB, source); !hit {
		t.Fatal("runtime cache publication unavailable")
	}
	var count int
	if err := runtime.DB.QueryRow(context.Background(), `SELECT count(*) FROM source_metadata_resolutions WHERE source_id=$1`, source.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("runtime lease DELETE did not complete")
	}
	_, err = runtime.DB.Exec(context.Background(), `CREATE TABLE forbidden_runtime_ddl(id integer)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatal("runtime DML grants unexpectedly permit DDL")
	}
}

func TestMetadataResolutionCoordinated007BackupRestorePreservesRowsAndMedia(t *testing.T) {
	container := os.Getenv("CUTMY_TEST_PGTOOLS_CONTAINER")
	if container == "" {
		t.Skip("set owned PostgreSQL17 tools container for real007 backup/restore")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,100}$`).MatchString(container) {
		t.Fatal("invalid owned tools container")
	}
	f := newMigrationFixture(t)
	s := openMigrationStore(t, f)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 0
	s.ConfigureStorage(c)
	local := Source{ID: newID("src"), Owner: "owner", Title: "Retained007", Kind: "upload", DurationMS: 10000, Path: writeLedgerFile(t, c, "sources", "retained007.mp4", 29)}
	if err := s.AddSource(context.Background(), local); err != nil {
		t.Fatal(err)
	}
	job, token := cleanupJob(t, s, local, false)
	job = finishCleanupJob(t, s, c, job, token, "succeeded")
	platform, info := storedMetadataSource(t, s)
	if err := s.CachePlatformMetadata(context.Background(), platform, info); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.claimPlatformMetadataResolution(context.Background(), platform, "retained007-claim"); err != nil || !claimed {
		t.Fatal("backup fixture claim failed")
	}
	before := metadataResolutionRecoveryRows(t, s.DB)
	parsed, err := url.Parse(f.url)
	if err != nil {
		t.Fatal(err)
	}
	user, database := parsed.User.Username(), strings.TrimPrefix(parsed.Path, "/")
	dump := cleanupPGTool(t, container, user, database, "pg_dump", nil, "--format=custom", "--schema", f.namespace, "--no-owner", "--no-privileges")
	media := cleanupMediaBackup(t, c.DataDir)
	restoredName := newID("metadata007_restore")
	sourcePinExec(t, f.db, "CREATE DATABASE "+pgx.Identifier{restoredName}.Sanitize())
	var restored *Store
	t.Cleanup(func() {
		if restored != nil {
			restored.DB.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.db.Exec(ctx, "DROP DATABASE "+pgx.Identifier{restoredName}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("owned007 restore database cleanup failed")
		}
	})
	cleanupPGTool(t, container, user, restoredName, "pg_restore", dump, "--exit-on-error", "--no-owner", "--no-privileges")
	cleanupMediaRestore(t, c.DataDir, media)
	restoredURL := *parsed
	restoredURL.Path = "/" + restoredName
	db, err := pgxpool.New(context.Background(), restoredURL.String())
	if err != nil {
		t.Fatal("owned restored pool failed")
	}
	restored = &Store{DB: db}
	if got := metadataResolutionRecoveryRows(t, db); got != before {
		t.Fatal("007 dump/restore changed claims/cache/ownership/queue/ledger rows")
	}
	physical := migrationSnapshot(t, db)
	compatible, err := OpenStore(context.Background(), restoredURL.String())
	if err != nil {
		t.Fatal("compatible007 restored startup failed")
	}
	defer compatible.DB.Close()
	compatible.ConfigureStorage(c)
	if got := migrationSnapshot(t, db); got != physical {
		t.Fatal("compatible007 startup rewrote restored rows/catalog")
	}
	if got := cleanupMediaBackup(t, c.DataDir); !bytes.Equal(got, media) {
		t.Fatal("coordinated007 restore changed media bytes")
	}
	if _, _, err := compatible.ArtifactPath(context.Background(), job.Items[0].Artifact.ID, job.Owner); err != nil {
		t.Fatal("restored007 completed artifact is unavailable")
	}
	if _, hit := readPlatformMetadataCache(context.Background(), compatible.DB, platform); !hit {
		t.Fatal("restored007 metadata cache was lost")
	}
}
