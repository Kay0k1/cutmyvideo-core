package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Install an actual historical catalog, not the current binary's latest schema.
func installPosterPredecessor(t *testing.T, f migrationFixture, count int) *Store {
	t.Helper()
	migrationExec(t, f.db, "CREATE TABLE app_schema_versions(version text PRIMARY KEY)")
	for _, m := range schemaMigrations[:count] {
		migrationExec(t, f.db, migrationSQL(m))
	}
	for i, m := range schemaMigrations[:count] {
		sourcePinExec(t, f.db, "INSERT INTO app_schema_versions(version) VALUES($1)", m.version)
		sourcePinExec(t, f.db, "INSERT INTO app_schema_migrations(position,version,checksum) VALUES($1,$2,$3)", i+1, m.version, migrationChecksum(m))
	}
	return &Store{DB: f.db}
}

func posterLegacyRows(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	rows := map[string]json.RawMessage{}
	for table := range expectedSchemaShape("migrations/009_schema.json").Tables {
		var b []byte
		query := `SELECT COALESCE(jsonb_agg(jsonb_build_object('row',to_jsonb(t),'xmin',t.xmin::text) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM ` + pgx.Identifier{table}.Sanitize() + ` t`
		if table == "app_schema_migrations" {
			query += " WHERE position<=9"
		}
		if table == "app_schema_versions" {
			query += " WHERE version<>'20261018-poster-artifacts-v1'"
		}
		if err := db.QueryRow(context.Background(), query).Scan(&b); err != nil {
			t.Fatal(err)
		}
		rows[table] = b
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPosterSchema009UpgradeAndPriorBinaryRefusal(t *testing.T) {
	f := newMigrationFixture(t)
	_ = installPosterPredecessor(t, f, 9)
	migrationExec(t, f.db, `INSERT INTO sources(id,owner,title,duration_ms,kind,path) VALUES('poster_legacy_source','owner','Preserve009',4000,'upload','/synthetic/source');
 INSERT INTO jobs(id,owner,source_id,request,items,status,idempotency_key) VALUES('poster_legacy_job','owner','poster_legacy_source','{"format":"mp4","ranges":[{"start_ms":0,"end_ms":2000}]}','[]','succeeded','preserve009');
 INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms) VALUES('poster_legacy_artifact','owner','poster_legacy_job','/synthetic/finished.mp4','finished.mp4',41,0,2000);
 INSERT INTO storage_files(path,owner,kind,resource_id,size_bytes) VALUES('/synthetic/finished.mp4','owner','artifact','poster_legacy_artifact',41);`)
	before := posterLegacyRows(t, f.db)
	s := openMigrationStore(t, f)
	if posterLegacyRows(t, s.DB) != before {
		t.Fatal("010 changed009 source/job/request/artifact/ledger bytes or row versions")
	}
	assertMigrationLedger(t, s)
	current := migrationSnapshot(t, s.DB)
	reopened := openMigrationStore(t, f)
	reopened.DB.Close()
	if migrationSnapshot(t, s.DB) != current {
		t.Fatal("current010 startup wrote catalog/history/application rows")
	}
	previous := os.Getenv("CUTMY_TEST_PREVIOUS_SCHEMA009_BINARY")
	if previous == "" {
		t.Skip("set exact signedv1.5.0 schema009 predecessor for010 refusal gate")
	}
	if !filepath.IsAbs(previous) {
		t.Fatal("previous binary must be absolute")
	}
	raw, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal("previous binary unavailable")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != os.Getenv("CUTMY_TEST_PREVIOUS_SCHEMA009_SHA256") {
		t.Fatal("previous signed binary digest mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, previous, "version").Output()
	var identity struct {
		Version, Commit, OS, Arch string
		GoVersion                 string `json:"go_version"`
	}
	if err != nil || json.Unmarshal(version, &identity) != nil || identity.Version != "v1.5.0" || identity.Commit != "ca334380ef6f8a8aa9e336b2817eb183501528b6" || identity.GoVersion != "go1.27.2" || identity.OS != runtime.GOOS || identity.Arch != runtime.GOARCH {
		t.Fatal("previous binary is not exact signed1.5.0")
	}
	command := exec.CommandContext(ctx, previous, "--json-errors", "maintenance")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "DATABASE_URL=" + f.url, "DATA_DIR=" + t.TempDir(), "GOMAXPROCS=2", "GOMEMLIMIT=128MiB"}
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil || json.Unmarshal(bytes.TrimSpace(output), &refusal) != nil || refusal.Error.Code != "schema_incompatible" {
		t.Fatal("signed009 binary did not refuse010 before processing; private output withheld")
	}
	if migrationSnapshot(t, s.DB) != current {
		t.Fatal("009 refusal changed010 catalog/history/rows")
	}
	after, err := os.ReadFile(previous)
	if err != nil || sha256.Sum256(after) != digest {
		t.Fatal("signed previous binary bytes changed")
	}
	// Unknown drift still refuses before touching the new catalog or old data.
	migrationExec(t, f.db, "DROP INDEX image_artifacts_expiry_idx")
	drifted := migrationSnapshot(t, s.DB)
	if rejected, err := OpenStore(context.Background(), f.url); !errors.Is(err, ErrSchemaIncompatible) {
		if rejected != nil {
			rejected.DB.Close()
		}
		t.Fatal("corrupt poster schema was silently accepted", err)
	}
	if migrationSnapshot(t, s.DB) != drifted {
		t.Fatal("corrupt-schema refusal wrote state")
	}
}

func TestPosterCoordinated010BackupRestorePreservesImages(t *testing.T) {
	container := os.Getenv("CUTMY_TEST_PGTOOLS_CONTAINER")
	if container == "" {
		t.Skip("set owned PostgreSQL tools container for coordinated010 restore")
	}
	s, server, cookie, source, parent := posterFixture(t)
	ctx := context.Background()
	r := posterHTTP(t, server, cookie, "POST", "/api/v1/artifacts/"+parent.ID+"/poster", `{"timestamp_ms":500,"format":"png"}`)
	if r.Code != 201 {
		t.Fatal("poster fixture", r.Code)
	}
	var image ImageArtifact
	if err := json.Unmarshal(r.Body.Bytes(), &image); err != nil {
		t.Fatal(err)
	}
	before := metadataResolutionRecoveryRows(t, s.DB)
	media := cleanupMediaBackup(t, server.Config.DataDir)
	parsed, err := url.Parse(s.DB.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal("synthetic URL unavailable")
	}
	user, database := parsed.User.Username(), strings.TrimPrefix(parsed.Path, "/")
	var namespace string
	if err = s.DB.QueryRow(ctx, "SELECT current_schema()").Scan(&namespace); err != nil {
		t.Fatal(err)
	}
	dump := cleanupPGTool(t, container, user, database, "pg_dump", nil, "--format=custom", "--schema", namespace, "--no-owner", "--no-privileges")
	name := newID("poster010_restore")
	sourcePinExec(t, s.DB, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	var restored *pgxpool.Pool
	t.Cleanup(func() {
		if restored != nil {
			restored.Close()
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := s.DB.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("owned010 restore database cleanup failed")
		}
	})
	cleanupPGTool(t, container, user, name, "pg_restore", dump, "--exit-on-error", "--no-owner", "--no-privileges")
	cleanupMediaRestore(t, server.Config.DataDir, media)
	copyURL := *parsed
	copyURL.Path = "/" + name
	restored, err = pgxpool.New(ctx, copyURL.String())
	if err != nil {
		t.Fatal("restored pool unavailable")
	}
	if metadataResolutionRecoveryRows(t, restored) != before {
		t.Fatal("010 restore changed parent/image/ledger/request rows")
	}
	snapshot := migrationSnapshot(t, restored)
	compatible, err := OpenStore(ctx, copyURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer compatible.DB.Close()
	if migrationSnapshot(t, restored) != snapshot {
		t.Fatal("restored010 startup changed state")
	}
	if !bytes.Equal(cleanupMediaBackup(t, server.Config.DataDir), media) {
		t.Fatal("010 restore changed completed image/media bytes")
	}
	restoredServer := NewServer(server.Config, compatible)
	download := posterHTTP(t, restoredServer, cookie, "GET", image.DownloadURL, "")
	if download.Code != 200 {
		t.Fatal("restored image unavailable", download.Code)
	}
	if _, _, err = compatible.ArtifactPath(ctx, parent.ID, source.Owner); err != nil {
		t.Fatal("restored parent unavailable", err)
	}
	r = posterHTTP(t, restoredServer, cookie, "POST", "/api/v1/artifacts/"+parent.ID+"/poster", `{"timestamp_ms":700}`)
	if r.Code != 201 {
		t.Fatal("new image after restore refused", r.Code, r.Body.String())
	}
}

func TestPosterRuntimeGrantsPermitLifecycleWithoutOwnership(t *testing.T) {
	admin, server, cookie, source, parent := posterFixture(t)
	ctx := context.Background()
	role, password := newID("poster_runtime"), newID("private")
	roleSQL := pgx.Identifier{role}.Sanitize()
	migrationExec(t, admin.DB, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+password+"'")
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.DB.Exec(cleanup, "DROP OWNED BY "+roleSQL)
		if _, err := admin.DB.Exec(cleanup, "DROP ROLE "+roleSQL); err != nil {
			t.Error("owned poster runtime role cleanup failed")
		}
	})
	var namespace, database string
	if err := admin.DB.QueryRow(ctx, "SELECT current_schema(),current_database()").Scan(&namespace, &database); err != nil {
		t.Fatal("owned fixture identity unavailable")
	}
	migrationExec(t, admin.DB, "GRANT USAGE ON SCHEMA "+pgx.Identifier{namespace}.Sanitize()+" TO "+roleSQL)
	migrationExec(t, admin.DB, "GRANT CONNECT ON DATABASE "+pgx.Identifier{database}.Sanitize()+" TO "+roleSQL)
	for table := range latestSchemaShape().Tables {
		privileges := "SELECT,INSERT,UPDATE,DELETE"
		if table == "app_schema_versions" || table == "app_schema_migrations" {
			privileges = "SELECT,MAINTAIN"
		}
		migrationExec(t, admin.DB, "GRANT "+privileges+" ON TABLE "+pgx.Identifier{namespace, table}.Sanitize()+" TO "+roleSQL)
	}
	parsed, err := url.Parse(admin.DB.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal("owned fixture URL unavailable")
	}
	parsed.User = url.UserPassword(role, password)
	runtimeStore, err := OpenStore(ctx, parsed.String())
	if err != nil {
		t.Fatal("poster DML runtime startup failed; private connection details withheld")
	}
	defer runtimeStore.DB.Close()
	var owns, create bool
	if err = runtimeStore.DB.QueryRow(ctx, `SELECT nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user),has_schema_privilege(current_schema(),'CREATE') FROM pg_namespace WHERE nspname=current_schema()`).Scan(&owns, &create); err != nil || owns || create {
		t.Fatal("poster runtime unexpectedly owns or can create schema")
	}
	runtimeServer := NewServer(server.Config, runtimeStore)
	r := posterHTTP(t, runtimeServer, cookie, "POST", "/api/v1/artifacts/"+parent.ID+"/poster", `{"timestamp_ms":500,"format":"png"}`)
	if r.Code != 201 {
		t.Fatal("runtime reader/reservation/image/ledger publication denied", r.Code)
	}
	var a ImageArtifact
	if err = json.Unmarshal(r.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if r = posterHTTP(t, runtimeServer, cookie, "GET", a.DownloadURL, ""); r.Code != 200 {
		t.Fatal("runtime image read denied", r.Code)
	}
	paths, err := runtimeStore.DeleteSource(ctx, source.ID, source.Owner)
	if err != nil {
		t.Fatal("runtime parent cascade/image tombstone denied", err)
	}
	if err = runtimeStore.DrainStorageDeletes(ctx, server.Config, paths); err != nil {
		t.Fatal(err)
	}
	if r = posterHTTP(t, runtimeServer, cookie, "GET", a.DownloadURL, ""); r.Code != 404 {
		t.Fatal("runtime cascade left image accessible", r.Code)
	}
	_, err = runtimeStore.DB.Exec(ctx, "CREATE TABLE forbidden_poster_ddl(id integer)")
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "42501" {
		t.Fatal("poster runtime DML grants unexpectedly permit DDL")
	}
}
