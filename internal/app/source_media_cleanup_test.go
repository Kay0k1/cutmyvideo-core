package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sourceCleanupFixture(t *testing.T) (*Store, Config, Source) {
	t.Helper()
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 0
	c.SourceTTL, c.ArtifactTTL = 24*time.Hour, 24*time.Hour
	s.ConfigureStorage(c)
	source := Source{ID: newID("src"), Owner: "owner", Title: "Keep my project", Kind: "upload", DurationMS: 10000, Path: writeLedgerFile(t, c, "sources", "original.mp4", 19), ThumbnailPath: writeLedgerFile(t, c, "sources", "thumbnail.jpg", 7)}
	if err := s.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	return s, c, source
}

func cleanupJob(t *testing.T, s *Store, source Source, cleanup bool) (Job, string) {
	t.Helper()
	request := requestFor(source)
	request.CleanupSource = cleanup
	j, err := s.CreateJob(context.Background(), source.Owner, request, "")
	if err != nil {
		t.Fatal(err)
	}
	token := newID("lease")
	sourcePinExec(t, s.DB, `UPDATE jobs SET status='running',lease_token=$2,lease_until=clock_timestamp()+interval '45 seconds' WHERE id=$1`, j.ID, token)
	j.Status = "running"
	return j, token
}

func finishCleanupJob(t *testing.T, s *Store, c Config, j Job, token, status string) Job {
	t.Helper()
	j.Status = status
	for i := range j.Items {
		j.Items[i].Status = status
		if status == "succeeded" {
			path := writeLedgerFile(t, c, "artifacts", j.Items[i].ID+".mp4", 13)
			a := Artifact{ID: newID("art"), Filename: "clip.mp4", DownloadURL: "/download", SizeBytes: 13}
			if err := s.AddArtifact(context.Background(), j.Owner, j.ID, path, token, a); err != nil {
				t.Fatal(err)
			}
			j.Items[i].Artifact = &a
		}
	}
	if err := s.SaveJob(context.Background(), j, token); err != nil {
		t.Fatal(err)
	}
	return j
}

func assertCleanupSource(t *testing.T, s *Store, source Source, removed bool) {
	t.Helper()
	got, err := s.Source(context.Background(), source.ID, source.Owner)
	if err != nil || got.MediaRemoved != removed || got.Title != source.Title {
		t.Fatalf("metadata lost or invalid state: %+v %v", got, err)
	}
	_, err = os.Stat(source.Path)
	if removed && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original not deleted: %v", err)
	}
	if !removed && err != nil {
		t.Fatalf("source prematurely deleted: %v", err)
	}
}

func TestSourceMediaCleanupKeepsMetadataJobsArtifactsAndDownloads(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	ctx := context.Background()
	cacheID := newID("preview")
	cacheDir := filepath.Join(c.DataDir, "previews", cacheID)
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Removal is generic: future waveform cache assets are covered too.
	if err := os.WriteFile(filepath.Join(cacheDir, "waveform.json"), []byte("waveform"), 0600); err != nil {
		t.Fatal(err)
	}
	sourcePinExec(t, s.DB, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id,token) VALUES($1,$2,'preview_cache',8,clock_timestamp()+interval '1 hour',$3,'waveform:fixture')`, cacheID, source.Owner, source.ID)
	j, token := cleanupJob(t, s, source, true)
	j = finishCleanupJob(t, s, c, j, token, "succeeded")
	if err := s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, true)
	if _, err := os.Stat(cacheDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cache survived", err)
	}
	if _, err := os.Stat(source.ThumbnailPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("thumbnail survived", err)
	}
	got, err := s.Job(ctx, j.ID, j.Owner)
	if err != nil || got.Items[0].Artifact == nil {
		t.Fatal("job/result lost", got, err)
	}
	path, _, err := s.ArtifactPath(ctx, j.Items[0].Artifact.ID, j.Owner)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 13 {
		t.Fatal("artifact bytes removed", err)
	}
	server := NewServer(c, s)
	req := httptest.NewRequest(http.MethodGet, "/download", nil)
	req.SetPathValue("id", j.Items[0].Artifact.ID)
	response := httptest.NewRecorder()
	server.download(response, req, j.Owner)
	if response.Code != http.StatusOK || response.Body.Len() != 13 {
		t.Fatal("result download stopped working", response.Code, response.Body.String())
	}
	for _, fn := range []ownerHandler{server.sourceMedia, server.sourceThumbnail, server.sourceTracks, server.sourcePreview} {
		req := httptest.NewRequest(http.MethodGet, "/source?start_ms=0", nil)
		req.SetPathValue("id", source.ID)
		response := httptest.NewRecorder()
		fn(response, req, source.Owner)
		if response.Code != http.StatusGone || !bytes.Contains(response.Body.Bytes(), []byte("source_removed")) {
			t.Fatal("missing removed diagnostic", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		fn(response, req, "stranger")
		if response.Code != http.StatusNotFound {
			t.Fatal("removal leaks other owner's source", response.Code)
		}
	}
	if _, err := s.CreateJobLimited(ctx, source.Owner, requestFor(source), "", 3, 10); !errors.Is(err, ErrSourceMediaRemoved) {
		t.Fatal("new export read deleted original", err)
	}
	var stored, reserved int64
	if err := s.DB.QueryRow(ctx, `SELECT stored_bytes::bigint,reserved_bytes::bigint FROM storage_counters WHERE id=1`).Scan(&stored, &reserved); err != nil || stored != 13 || reserved != 0 {
		t.Fatal("accounting disagrees after deletion", stored, reserved, err)
	}
	// Media removal has no hold on artifact retention.
	sourcePinExec(t, s.DB, `UPDATE artifacts SET expires_at=now()-interval '1 second' WHERE id=$1`, j.Items[0].Artifact.ID)
	if _, err := s.Cleanup(ctx, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ArtifactPath(ctx, j.Items[0].Artifact.ID, j.Owner); !errors.Is(err, ErrNotFound) {
		t.Fatal("source metadata pins expired output", err)
	}
}

func TestSourceMediaCleanupWaitsForReadersPreviewsAndAcceptedJobs(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	ctx := context.Background()
	server := NewServer(c, s)
	readCtx, release, err := server.pinSourceMediaReader(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	preview := newID("preview")
	sourcePinExec(t, s.DB, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES($1,$2,'preview',0,clock_timestamp()+interval '5 minutes',$3)`, preview, source.Owner, source.ID)
	first, token := cleanupJob(t, s, source, true)
	accepted, secondToken := cleanupJob(t, s, source, false)
	finishCleanupJob(t, s, c, first, token, "succeeded")
	if err := s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
	if readCtx.Err() != nil {
		t.Fatal("cleanup stopped active read")
	}
	if _, _, err := server.pinSourceMediaReader(ctx, source); !errors.Is(err, ErrSourceMediaRemoved) {
		t.Fatal("new reader ignored pending marker", err)
	}
	if err := s.reserveSourceInspection(ctx, source, newID("inspect"), time.Second); !errors.Is(err, ErrSourceMediaRemoved) {
		t.Fatal("inspection ignored pending marker", err)
	}
	finishCleanupJob(t, s, c, accepted, secondToken, "succeeded")
	if err := s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
	sourcePinExec(t, s.DB, `DELETE FROM storage_reservations WHERE id=$1`, preview)
	if err := s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
	release()
	if err := s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, true)
}

func TestSourceMediaCleanupFailureCancellationAndLegacyKeepSource(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			s, c, source := sourceCleanupFixture(t)
			j, token := cleanupJob(t, s, source, status != "succeeded")
			finishCleanupJob(t, s, c, j, token, status)
			if err := s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			assertCleanupSource(t, s, source, false)
		})
	}
	for _, status := range []string{"failed", "cancelled"} {
		t.Run("concurrent-"+status, func(t *testing.T) {
			s, c, source := sourceCleanupFixture(t)
			first, token := cleanupJob(t, s, source, true)
			other, otherToken := cleanupJob(t, s, source, false)
			finishCleanupJob(t, s, c, first, token, "succeeded")
			finishCleanupJob(t, s, c, other, otherToken, status)
			if err := s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			assertCleanupSource(t, s, source, false)
			// Old failures do not prevent a later successful cleanup request.
			retry, retryToken := cleanupJob(t, s, source, true)
			finishCleanupJob(t, s, c, retry, retryToken, "succeeded")
			if err := s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			assertCleanupSource(t, s, source, true)
		})
	}
}

func TestSourceMediaCleanupPartialSuccessAndCancellationRaceKeepSource(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	j, token := cleanupJob(t, s, source, true)
	j.Status = "succeeded"
	j.Items[0].Status = "succeeded"
	path := writeLedgerFile(t, c, "artifacts", "partial.mp4", 13)
	a := Artifact{ID: newID("art"), Filename: "partial.mp4", SizeBytes: 13}
	if err := s.AddArtifact(context.Background(), j.Owner, j.ID, path, token, a); err != nil {
		t.Fatal(err)
	}
	j.Items[0].Artifact = &a
	j.Items = append(j.Items, JobItem{ID: newID("item"), Status: "failed"})
	// A malformed success snapshot must not schedule cleanup either.
	if err := s.SaveJob(context.Background(), j, token); err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
	cancelJob, cancelToken := cleanupJob(t, s, source, true)
	output := writeLedgerFile(t, c, "artifacts", "cancel-race.mp4", 13)
	artifact := Artifact{ID: newID("art"), Filename: "cancel-race.mp4", SizeBytes: 13}
	if err := s.AddArtifact(context.Background(), cancelJob.Owner, cancelJob.ID, output, cancelToken, artifact); err != nil {
		t.Fatal(err)
	}
	cancelJob.Status = "succeeded"
	cancelJob.Items[0].Status = "succeeded"
	cancelJob.Items[0].Artifact = &artifact
	if err := s.Cancel(context.Background(), cancelJob.ID, cancelJob.Owner); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveJob(context.Background(), cancelJob, cancelToken); err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
}

func TestSourceMediaCleanupRestartTombstoneChargeAndPlatformUnavailable(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	ctx := context.Background()
	j, token := cleanupJob(t, s, source, true)
	finishCleanupJob(t, s, c, j, token, "succeeded")
	paths, err := s.prepareSourceMediaCleanup(ctx, c, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored int64
	if err = s.DB.QueryRow(ctx, `SELECT stored_bytes::bigint FROM storage_counters WHERE id=1`).Scan(&stored); err != nil || stored != 39 {
		t.Fatal("tombstone released charge before physical deletion", stored, err)
	}
	if _, err = os.Stat(source.Path); err != nil {
		t.Fatal("prepare deleted original before committed tombstone drain", err)
	}
	// A separate Store against the same durable DB can finish lost acknowledgements.
	restarted := &Store{DB: s.DB}
	restarted.ConfigureStorage(c)
	if err = restarted.DrainStorageDeletes(ctx, c, paths); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, restarted, source, true)
	platform := Source{ID: newID("src"), Owner: source.Owner, Title: "Public recording", Kind: "platform", URL: "https://example.com/video", DurationMS: 10000, EmbedURL: pointerString("https://example.com/embed")}
	if err = s.AddSource(ctx, platform); err != nil {
		t.Fatal(err)
	}
	job, platformToken := cleanupJob(t, s, platform, true)
	finishCleanupJob(t, s, c, job, platformToken, "succeeded")
	if err = s.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Source(ctx, platform.ID, platform.Owner)
	if err != nil || !got.MediaRemoved || got.PreviewKind != "none" || got.EmbedURL != nil {
		t.Fatal("platform was made refetchable after cleanup", got, err)
	}
}

func TestSourceMediaCleanupJobAdmissionAndCleanupShareFence(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	j, token := cleanupJob(t, s, source, true)
	finishCleanupJob(t, s, c, j, token, "succeeded")
	lock, err := s.storageTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	admitted := make(chan error, 1)
	go func() {
		_, err := s.CreateJobLimited(context.Background(), source.Owner, requestFor(source), "", 3, 10)
		admitted <- err
	}()
	select {
	case err := <-admitted:
		t.Fatal("admission bypassed fence", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err = lock.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = <-admitted; !errors.Is(err, ErrSourceMediaRemoved) {
		t.Fatal("late admission passed durable marker", err)
	}
	if err = s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, true)
}

func TestSourceMediaCleanupMigrationUpgradeKeepsOldData(t *testing.T) {
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
	migrationExec(t, f.db, historicalRows)
	s := openMigrationStore(t, f)
	assertMigrationLedger(t, s)
	var removed bool
	if err := s.DB.QueryRow(context.Background(), `SELECT media_removed FROM sources LIMIT 1`).Scan(&removed); err != nil || removed {
		t.Fatal("upgrade removed old media", removed, err)
	}
	// Current startup remains read-only, including new lifecycle state.
	before := migrationSnapshot(t, s.DB)
	_ = openMigrationStore(t, f)
	if after := migrationSnapshot(t, s.DB); after != before {
		t.Fatal("current startup mutated data")
	}
	tx, err := s.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	shape, err := readSchemaShape(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaShape(shape, expectedSchemaShape(schemaMigrations[4].shape), false); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatal("old shape accepted cleanup schema", err)
	}
}

func TestSourceMediaCleanupJSONCompatibility(t *testing.T) {
	var request ExportRequest
	if err := json.Unmarshal([]byte(`{"source_id":"src"}`), &request); err != nil || request.CleanupSource {
		t.Fatal("legacy request opted into deletion", err)
	}
	// Reader callbacks stop accessing data after uncertain renewal/deadline.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := contextReadSeeker{ctx, bytes.NewReader([]byte("private"))}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal("expired read continued", err)
	}
}

func TestSourceMediaCleanupRecoversPendingRequestAfterReaderCrash(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	ctx := context.Background()
	id := newID("reader")
	if _, err := s.acquireSourceMediaReader(ctx, source, id); err != nil {
		t.Fatal(err)
	}
	j, token := cleanupJob(t, s, source, true)
	finishCleanupJob(t, s, c, j, token, "succeeded")
	restarted := &Store{DB: s.DB}
	restarted.ConfigureStorage(c)
	if err := restarted.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
	sourcePinExec(t, s.DB, `UPDATE source_media_readers SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id)
	if err := restarted.ProcessSourceMediaCleanup(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, true)
}

func TestSourceMediaCleanupPreservesEarlierOverlappingFailure(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	earlier, earlierToken := cleanupJob(t, s, source, false)
	requested, token := cleanupJob(t, s, source, true)
	finishCleanupJob(t, s, c, earlier, earlierToken, "failed")
	finishCleanupJob(t, s, c, requested, token, "succeeded")
	if err := s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, false)
	retry, retryToken := cleanupJob(t, s, source, true)
	finishCleanupJob(t, s, c, retry, retryToken, "succeeded")
	if err := s.ProcessSourceMediaCleanup(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertCleanupSource(t, s, source, true)
}
