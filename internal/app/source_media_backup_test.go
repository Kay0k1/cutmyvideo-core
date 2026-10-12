package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Real pg_dump/pg_restore uses PostgreSQL17 tools from the owned test service.
// No host package install or external service/database is performed by the test.
func cleanupPGTool(t *testing.T, container, user, database, tool string, input []byte, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := []string{"exec", "-i", container, tool, "--username", user, "--dbname", database}
	command = append(command, args...)
	cmd := exec.CommandContext(ctx, "docker", command...)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("owned PostgreSQL backup/restore tool failed: %s: %v", tool, err)
	}
	return output
}

func cleanupMediaBackup(t *testing.T, root string) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	archive := tar.NewWriter(compressed)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected media entry")
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if err = archive.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: 0600, Size: info.Size()}); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(archive, file)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err = compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
func cleanupMediaRestore(t *testing.T, root string, backup []byte) {
	t.Helper()
	// Only this fixture's owned temporary root is replaced, with writers stopped.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(backup))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsLocal(header.Name) || header.Typeflag != tar.TypeReg || header.Size > 1<<20 {
			t.Fatal("unsafe fixture archive")
		}
		path := filepath.Join(root, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(file, archive)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
}
func cleanupBackupRows(t *testing.T, db *pgxpool.Pool, include006 bool) string {
	t.Helper()
	tables := []string{"sources", "jobs", "artifacts", "storage_files", "storage_reservations", "storage_counters", "app_schema_versions", "app_schema_migrations"}
	if include006 {
		tables = append(tables, "source_media_cleanup", "source_media_readers")
	}
	result := map[string]json.RawMessage{}
	for _, table := range tables {
		var rows []byte
		if err := db.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		result[table] = rows
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSourceMediaCleanupCoordinated005ReverseAndCompatible006BackupRestore(t *testing.T) {
	container := os.Getenv("CUTMY_TEST_PGTOOLS_CONTAINER")
	if container == "" {
		t.Skip("set CUTMY_TEST_PGTOOLS_CONTAINER to owned PostgreSQL17 test service for real coordinated backup/restore")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,100}$`).MatchString(container) {
		t.Fatal("invalid owned test PostgreSQL container identity")
	}
	f := newMigrationFixture(t)
	migrationExec(t, f.db, `CREATE TABLE app_schema_versions(version text PRIMARY KEY)`)
	for i, m := range schemaMigrations[:5] {
		migrationExec(t, f.db, migrationSQL(m))
		sourcePinExec(t, f.db, `INSERT INTO app_schema_versions(version)VALUES($1)`, m.version)
		if i == 1 {
			for n := 0; n < 2; n++ {
				sourcePinExec(t, f.db, `INSERT INTO app_schema_migrations(position,version,checksum)VALUES($1,$2,$3)`, n+1, schemaMigrations[n].version, migrationChecksum(schemaMigrations[n]))
			}
		}
		if i >= 2 {
			sourcePinExec(t, f.db, `INSERT INTO app_schema_migrations(position,version,checksum)VALUES($1,$2,$3)`, i+1, m.version, migrationChecksum(m))
		}
	}
	parsed, err := url.Parse(f.url)
	if err != nil {
		t.Fatal(err)
	}
	user := parsed.User.Username()
	database := parsed.Path[1:]
	old := &Store{DB: f.db}
	c := ledgerConfig(t)
	c.MaxStorageBytes = 0
	c.SourceTTL, c.ArtifactTTL = time.Hour, time.Hour
	old.ConfigureStorage(c)
	first := Source{ID: newID("src"), Owner: "owner", Title: "Original005", Kind: "upload", DurationMS: 10000, Path: writeLedgerFile(t, c, "sources", "first.mp4", 19)}
	second := Source{ID: newID("src"), Owner: "owner", Title: "Retained005", Kind: "upload", DurationMS: 10000, Path: writeLedgerFile(t, c, "sources", "second.mp4", 23)}
	for _, source := range []Source{first, second} {
		if err = old.AddSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	oldRows := cleanupBackupRows(t, f.db, false)
	dump005 := cleanupPGTool(t, container, user, database, "pg_dump", nil, "--format=custom", "--schema", f.namespace, "--no-owner", "--no-privileges")
	media005 := cleanupMediaBackup(t, c.DataDir)
	current := openMigrationStore(t, f)
	current.ConfigureStorage(c)
	firstJob, token := cleanupJob(t, current, first, true)
	firstJob = finishCleanupJob(t, current, c, firstJob, token, "succeeded")
	if err = current.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, current, first, true)
	// A second cleanup is pending at the backup boundary, held by an active read.
	reader := newID("reader")
	if _, err = current.acquireSourceMediaReader(context.Background(), second, reader); err != nil {
		t.Fatal(err)
	}
	secondJob, secondToken := cleanupJob(t, current, second, true)
	secondJob = finishCleanupJob(t, current, c, secondJob, secondToken, "succeeded")
	snapshot006 := cleanupBackupRows(t, current.DB, true)
	dump006 := cleanupPGTool(t, container, user, database, "pg_dump", nil, "--format=custom", "--schema", f.namespace, "--no-owner", "--no-privileges")
	media006 := cleanupMediaBackup(t, c.DataDir)
	restoreDatabase := func(label string, dump []byte) (*Store, string) {
		name := newID("cleanup_restore_" + label)
		sourcePinExec(t, f.db, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
		restoredURL := *parsed
		restoredURL.Path = "/" + name
		cleanupPGTool(t, container, user, name, "pg_restore", dump, "--exit-on-error", "--no-owner", "--no-privileges")
		db, err := pgxpool.New(context.Background(), restoredURL.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := f.db.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Error("owned restore database cleanup failed", err)
			}
		})
		store := &Store{DB: db}
		store.ConfigureStorage(c)
		return store, restoredURL.String()
	}
	// An actual coordinated reverse restores both original005 media and schema.
	reversed, _ := restoreDatabase("005", dump005)
	cleanupMediaRestore(t, c.DataDir, media005)
	if got := cleanupBackupRows(t, reversed.DB, false); got != oldRows {
		t.Fatal("fresh005 pg_restore changed original records")
	}
	tx, err := reversed.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shape, err := readSchemaShape(context.Background(), tx)
	rollbackStorage(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaShape(shape, expectedSchemaShape(schemaMigrations[4].shape), false); err != nil {
		t.Fatal("fresh005 restore is not compatible with declared005 catalog", err)
	}
	for _, source := range []Source{first, second} {
		if _, err = os.Stat(source.Path); err != nil {
			t.Fatal("coordinated005 reverse lost media", err)
		}
	}
	// Restore006 uses its own captured media snapshot and compatible006 startup.
	restored, restoredURL := restoreDatabase("006", dump006)
	cleanupMediaRestore(t, c.DataDir, media006)
	if got := cleanupBackupRows(t, restored.DB, true); got != snapshot006 {
		t.Fatal("006 restore changed tombstones/pending cleanup/ownership/records")
	}
	compatible, err := OpenStore(context.Background(), restoredURL)
	if err != nil {
		t.Fatal(err)
	}
	defer compatible.DB.Close()
	compatible.ConfigureStorage(c)
	if got := cleanupBackupRows(t, compatible.DB, true); got != snapshot006 {
		t.Fatal("compatible006 startup rewrote restored data")
	}
	// Independent output downloads remain usable despite the removed original.
	server := NewServer(c, compatible)
	for _, job := range []Job{firstJob, secondJob} {
		request := httptest.NewRequest(http.MethodGet, "/download", nil)
		request.SetPathValue("id", job.Items[0].Artifact.ID)
		response := httptest.NewRecorder()
		server.download(response, request, job.Owner)
		if response.Code != http.StatusOK || response.Body.Len() != 13 {
			t.Fatal("006 restored artifact unavailable", response.Code)
		}
	}
	sourcePinExec(t, compatible.DB, `UPDATE source_media_readers SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, reader)
	if err = compatible.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, compatible, second, true)
	for _, job := range []Job{firstJob, secondJob} {
		if _, _, err = compatible.ArtifactPath(context.Background(), job.Items[0].Artifact.ID, job.Owner); err != nil {
			t.Fatal("resumed cleanup removed result", err)
		}
	}
}

func TestSourceMediaReaderRetentionIsBoundedWithoutCounterQuarantine(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	ctx := context.Background()
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, source.ID)
	reader := newID("reader")
	if _, err := s.acquireSourceMediaReader(ctx, source, reader); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cleanup(ctx, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
	assertSourcePins(t, s, source.ID, 0, 0, true)
	sourcePinExec(t, s.DB, `DELETE FROM source_media_readers WHERE id=$1`, reader)
	paths, err := s.Cleanup(ctx, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DrainStorageDeletes(ctx, c, paths); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Source(ctx, source.ID, source.Owner); err != ErrNotFound {
		t.Fatal("released reader permanently retained source", err)
	}
}
