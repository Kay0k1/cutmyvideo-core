package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema007ReleaseCommit = "e4a0477b727ccfbbf21f86027fdd07f6ffe78419"

type agentRecoveryFixture struct {
	project   AgentProject
	source    Source
	recipient *http.Cookie
	token     string
	jobID     string
}

func seedAgentRecovery(t *testing.T, store *Store, config Config, sourcePath string) agentRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	server := NewServer(config, store)
	creator := agentProjectCookie(111)
	recipient := agentProjectCookie(112)
	r := &http.Request{Header: make(http.Header)}
	r.AddCookie(creator)
	owner, _ := ownerFromRequest(r)
	intent := sourcePreparationIntent{Kind: "import", URL: "https://media.example/retained-schema8"}
	fingerprint := &SourceFingerprint{Kind: "provider", CanonicalURLSHA256: strings.Repeat("a", 64)}
	if sourcePath != "" {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fingerprint = &SourceFingerprint{Kind: "content", SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data))}
		intent = sourcePreparationIntent{Kind: "upload", SHA256: fingerprint.SHA256, SizeBytes: fingerprint.SizeBytes}
	}
	claim, err := store.claimSourcePreparation(ctx, owner, "retained-preparation", intent, time.Minute)
	if err != nil || claim == nil {
		t.Fatal("could not seed source preparation")
	}
	source := Source{ID: claim.sourceID, Owner: owner, Title: "Retained schema8 source", Kind: "platform", DurationMS: 4000, Width: 160, Height: 96, Fingerprint: fingerprint}
	if sourcePath != "" {
		source.Kind, source.Path = "upload", sourcePath
	}
	claim.attach(&source)
	if err = store.AddSource(ctx, source); err != nil {
		t.Fatal("could not publish retained preparation", err)
	}
	plan := CollectionPlan{Version: 1, Options: CollectionOptions{Format: "mp4", Quality: "best", CutMode: "accurate"}, Clips: []CollectionClip{{ID: "opening", Label: "Opening", Ranges: []Range{{StartMS: 1000, EndMS: 2000}}}}}
	prepared, err := projectPrepared(plan, nil, "final", source, config, true)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, owner, prepared[0].Request, collectionJobKey("schema8-root", prepared[0]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(ctx, `UPDATE jobs SET status='failed',items=(SELECT jsonb_agg(item||'{"status":"failed"}'::jsonb) FROM jsonb_array_elements(items) item) WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	project := agentProjectDecode(t, agentProjectRequest(t, server, creator, "POST", "/api/v1/projects", projectCreateRequest{SourceID: source.ID, Plan: plan, CollectionKey: "schema8-root", JobIDs: []string{job.ID}}, nil), 201)
	base := "/api/v1/projects/" + project.ID
	issue := agentProjectRequest(t, server, creator, "POST", base+"/handoffs", struct{}{}, nil)
	var handoff struct {
		Token string `json:"token"`
	}
	if issue.Code != 201 || json.Unmarshal(issue.Body.Bytes(), &handoff) != nil {
		t.Fatal("could not seed narrow handoff")
	}
	if w := agentProjectRequest(t, server, recipient, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 200 {
		t.Fatal("could not seed recipient grant", w.Code)
	}
	result := collectionSubmissionReport(t, agentProjectRequest(t, server, recipient, "POST", base+"/collections/retry", map[string]any{"revision": project.Revision, "clip_ids": []string{"opening"}}, map[string]string{"Idempotency-Key": "retained-schema8-attempt"}), 202)
	return agentRecoveryFixture{project: project, source: source, recipient: recipient, token: handoff.Token, jobID: result.Clips[0].Job.ID}
}

func agentRecoveryConfig(t *testing.T) Config {
	t.Helper()
	c := mediaConfig(t)
	c.MaxStorageBytes = 0
	c.MaxOwnerBytes = 64 << 20
	c.MaxActiveJobs = 32
	c.MutationsPerMinute = 200
	c.FFmpegThreads = 1
	c.SourceTimeout, c.JobTimeout = 20*time.Second, 20*time.Second
	c.SourceTTL, c.ArtifactTTL = time.Hour, time.Hour
	return c
}

func TestAgentProjectsPreviousBinaryRefuses008WithoutWrites(t *testing.T) {
	previous := os.Getenv("CUTMY_TEST_PREVIOUS_SCHEMA007_BINARY")
	if previous == "" {
		t.Skip("set exact owned v1.3.0 schema7 binary for required previous-image refusal gate")
	}
	if !filepath.IsAbs(previous) {
		t.Fatal("previous schema7 binary must be absolute")
	}
	data, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal("previous schema7 executable unavailable")
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	if digest != os.Getenv("CUTMY_TEST_PREVIOUS_SCHEMA007_SHA256") {
		t.Fatal("previous schema7 executable differs from prepared source-build receipt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, previous, "version").Output()
	var identity struct{ Version, Commit, GoVersion, OS, Arch string }
	// JSON uses snake_case for the toolchain field.
	var toolchain struct {
		GoVersion string `json:"go_version"`
	}
	if err != nil || json.Unmarshal(version, &identity) != nil || json.Unmarshal(version, &toolchain) != nil || identity.Version != "v1.3.0" || identity.Commit != schema007ReleaseCommit || toolchain.GoVersion != "go1.27.2" || identity.OS != runtime.GOOS || identity.Arch != runtime.GOARCH {
		t.Fatal("previous binary is not the exact v1.3.0 schema7 source/toolchain/target")
	}
	f := newMigrationFixture(t)
	current := openMigrationStore(t, f)
	seedAgentRecovery(t, current, agentRecoveryConfig(t), "")
	before := migrationSnapshot(t, current.DB)
	cmd := exec.CommandContext(ctx, previous, "worker")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DATABASE_URL=" + f.url, "DATA_DIR=" + t.TempDir(), "GOMAXPROCS=2", "GOMEMLIMIT=128MiB"}
	output, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !bytes.Contains(output, []byte("database schema is incompatible")) {
		t.Fatal("v1.3.0 did not refuse schema8 before processing; private command output withheld")
	}
	if after := migrationSnapshot(t, current.DB); after != before {
		t.Fatal("schema7 refusal changed schema8 catalog, row versions, migration history or grants")
	}
	_ = openMigrationStore(t, f)
	if after := migrationSnapshot(t, current.DB); after != before {
		t.Fatal("compatible schema8 startup rewrote retained state")
	}
	after, err := os.ReadFile(previous)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("previous binary changed during refusal proof")
	}
}

func TestAgentProjectsRuntimeGrantsPermitCRUDWithoutOwnership(t *testing.T) {
	f := newMigrationFixture(t)
	admin := openMigrationStore(t, f)
	role, password := newID("agent_runtime"), newID("private")
	roleSQL := pgx.Identifier{role}.Sanitize()
	namespace := pgx.Identifier{f.namespace}.Sanitize()
	migrationExec(t, f.db, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+password+"'")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = f.db.Exec(ctx, "DROP OWNED BY "+roleSQL)
		if _, err := f.db.Exec(ctx, "DROP ROLE "+roleSQL); err != nil {
			t.Error("owned schema8 runtime role cleanup failed")
		}
	})
	migrationExec(t, f.db, "GRANT USAGE ON SCHEMA "+namespace+" TO "+roleSQL)
	var database string
	if err := f.db.QueryRow(context.Background(), `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal("owned database identity unavailable")
	}
	migrationExec(t, f.db, "GRANT CONNECT ON DATABASE "+pgx.Identifier{database}.Sanitize()+" TO "+roleSQL)
	for table := range latestSchemaShape().Tables {
		privileges := "SELECT, INSERT, UPDATE, DELETE"
		if table == "app_schema_versions" || table == "app_schema_migrations" {
			privileges = "SELECT, MAINTAIN"
		}
		migrationExec(t, f.db, "GRANT "+privileges+" ON TABLE "+namespace+"."+pgx.Identifier{table}.Sanitize()+" TO "+roleSQL)
	}
	parsed, err := url.Parse(f.url)
	if err != nil {
		t.Fatal("owned database URL parsing failed")
	}
	parsed.User = url.UserPassword(role, password)
	before := migrationSnapshot(t, admin.DB)
	limited, err := OpenStore(context.Background(), parsed.String())
	if err != nil {
		t.Fatal("restricted schema8 runtime startup failed; private connection details withheld")
	}
	defer limited.DB.Close()
	if after := migrationSnapshot(t, admin.DB); after != before {
		t.Fatal("restricted current startup wrote catalog or rows")
	}
	var owns, create bool
	if err = limited.DB.QueryRow(context.Background(), `SELECT n.nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user),has_schema_privilege(current_schema(),'CREATE') FROM pg_namespace n WHERE n.nspname=current_schema()`).Scan(&owns, &create); err != nil || owns || create {
		t.Fatal("restricted runtime unexpectedly owns or can create application schema")
	}
	fixture := seedAgentRecovery(t, limited, agentRecoveryConfig(t), "")
	server := NewServer(limited.storageConfig(), limited)
	if w := agentProjectRequest(t, server, fixture.recipient, "GET", "/api/v1/projects/"+fixture.project.ID, nil, nil); w.Code != 200 {
		t.Fatal("restricted runtime cannot read its project grant")
	}
	ctx := context.Background()
	tx, err := limited.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	tables := []struct{ name, column string }{{"agent_project_retries", "intent_hash"}, {"agent_project_grants", "expires_at"}, {"agent_project_handoffs", "expires_at"}, {"agent_project_jobs", "clip_id"}, {"agent_projects", "revision"}, {"source_fingerprints", "owner"}, {"source_preparations", "error_code"}}
	for _, table := range tables {
		name := pgx.Identifier{table.name}.Sanitize()
		column := pgx.Identifier{table.column}.Sanitize()
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&count); err != nil || count < 1 {
			t.Fatal("restricted runtime new-table SELECT failed", table.name)
		}
		tag, e := tx.Exec(ctx, "UPDATE "+name+" SET "+column+"="+column)
		if e != nil || tag.RowsAffected() < 1 {
			t.Fatal("restricted runtime new-table UPDATE failed", table.name)
		}
		tag, e = tx.Exec(ctx, "DELETE FROM "+name)
		if e != nil || tag.RowsAffected() < 1 {
			t.Fatal("restricted runtime new-table DELETE failed", table.name)
		}
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = limited.DB.Exec(ctx, `CREATE TABLE forbidden_agent_runtime_ddl(id integer)`)
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatal("schema8 runtime DML unexpectedly authorized DDL")
	}
}

func TestAgentProjectsCoordinated008BackupRestorePreservesRowsAndMedia(t *testing.T) {
	container := os.Getenv("CUTMY_TEST_PGTOOLS_CONTAINER")
	if container == "" {
		t.Skip("set owned PostgreSQL17 tools container for required schema8 coordinated restore")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,100}$`).MatchString(container) {
		t.Fatal("invalid owned PostgreSQL tools container")
	}
	f := newMigrationFixture(t)
	store := openMigrationStore(t, f)
	config := agentRecoveryConfig(t)
	config.MaxStorageBytes, config.MaxOwnerBytes = 128<<20, 128<<20
	for _, dir := range []string{"sources", "work", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(config.DataDir, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(config.DataDir, "sources", "schema8-source.mp4")
	mcRun(t, context.Background(), config, "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25", "-t", "4", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", sourcePath)
	fixture := seedAgentRecovery(t, store, config, sourcePath)
	job, token, err := store.Claim(context.Background())
	if err != nil || job.ID != fixture.jobID {
		t.Fatal("retained queued repair unavailable")
	}
	processJob(context.Background(), config, store, job, token)
	completed, err := store.Job(context.Background(), job.ID, job.Owner)
	if err != nil || completed.Status != "succeeded" || len(completed.Items) != 1 || completed.Items[0].Artifact == nil {
		t.Fatal("schema8 recovery fixture did not render actual media")
	}
	artifact := *completed.Items[0].Artifact
	artifactPath, _, err := store.ArtifactPath(context.Background(), artifact.ID, job.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runCommand(context.Background(), config.FFmpeg, "-v", "error", "-i", artifactPath, "-f", "null", "-"); err != nil {
		t.Fatal("schema8 backup artifact did not fully decode")
	}
	before := metadataResolutionRecoveryRows(t, store.DB)
	media := cleanupMediaBackup(t, config.DataDir)
	parsed, err := url.Parse(f.url)
	if err != nil {
		t.Fatal("owned database URL parsing failed")
	}
	user, database := parsed.User.Username(), strings.TrimPrefix(parsed.Path, "/")
	dump := cleanupPGTool(t, container, user, database, "pg_dump", nil, "--format=custom", "--schema", f.namespace, "--no-owner", "--no-privileges")
	name := newID("agent008_restore")
	sourcePinExec(t, f.db, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	var restored *pgxpool.Pool
	t.Cleanup(func() {
		if restored != nil {
			restored.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.db.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("owned schema8 restore database cleanup failed")
		}
	})
	cleanupPGTool(t, container, user, name, "pg_restore", dump, "--exit-on-error", "--no-owner", "--no-privileges")
	cleanupMediaRestore(t, config.DataDir, media)
	copyURL := *parsed
	copyURL.Path = "/" + name
	restored, err = pgxpool.New(context.Background(), copyURL.String())
	if err != nil {
		t.Fatal("owned restored database pool failed")
	}
	if actual := metadataResolutionRecoveryRows(t, restored); actual != before {
		t.Fatal("schema8 restore changed source receipt/fingerprint/project/grant/retry/job/artifact or ledger rows")
	}
	physical := migrationSnapshot(t, restored)
	compatible, err := OpenStore(context.Background(), copyURL.String())
	if err != nil {
		t.Fatal("compatible schema8 restored startup failed")
	}
	defer compatible.DB.Close()
	compatible.ConfigureStorage(config)
	if after := migrationSnapshot(t, restored); after != physical {
		t.Fatal("compatible schema8 startup changed restored row versions/catalog")
	}
	if after := cleanupMediaBackup(t, config.DataDir); !bytes.Equal(after, media) {
		t.Fatal("coordinated schema8 restore changed media bytes")
	}
	receipt, err := compatible.SourcePreparation(context.Background(), fixture.source.Owner, "retained-preparation")
	if err != nil || receipt.State != "ready" || receipt.Source == nil || receipt.Fingerprint == nil || *receipt.Fingerprint != *fixture.source.Fingerprint {
		t.Fatal("restored durable source preparation is unavailable")
	}
	server := NewServer(config, compatible)
	p := agentProjectDecode(t, agentProjectRequest(t, server, fixture.recipient, "GET", "/api/v1/projects/"+fixture.project.ID, nil, nil), 200)
	if len(p.Jobs) != 1 || !p.Jobs[0].Current || p.Jobs[0].Job.ID != fixture.jobID {
		t.Fatal("restored scoped recipient lost the exact repaired project job")
	}
	if w := agentProjectRequest(t, server, fixture.recipient, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": fixture.token}, nil); w.Code != 200 {
		t.Fatal("restored recipient cannot recover a lost handoff reply")
	}
	retry := collectionSubmissionReport(t, agentProjectRequest(t, server, fixture.recipient, "POST", "/api/v1/projects/"+p.ID+"/collections/retry", map[string]any{"revision": p.Revision, "clip_ids": []string{"opening"}}, map[string]string{"Idempotency-Key": "retained-schema8-attempt"}), 202)
	if retry.Clips[0].Job.ID != fixture.jobID {
		t.Fatal("restored retry intent created duplicate work")
	}
	path, _, err := compatible.ArtifactPath(context.Background(), artifact.ID, job.Owner)
	if err != nil {
		t.Fatal("restored actual artifact unavailable")
	}
	if _, err = runCommand(context.Background(), config.FFmpeg, "-v", "error", "-i", path, "-f", "null", "-"); err != nil {
		t.Fatal("restored actual artifact did not fully decode")
	}
	var count int
	if err = compatible.DB.QueryRow(context.Background(), `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 2 {
		t.Fatal("restore recovery rerendered a retained clip", count)
	}
}
