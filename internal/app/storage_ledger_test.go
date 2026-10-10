package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func ledgerConfig(t *testing.T) Config {
	return Config{DataDir: t.TempDir(), MaxSourceBytes: 4 << 20, MaxOutputBytes: 4 << 20, MaxStorageBytes: 16 << 20, MaxOwnerBytes: 32 << 20, MaxActiveJobs: 32, SourceTimeout: time.Second, JobTimeout: time.Minute, SourceTTL: time.Hour, ArtifactTTL: time.Hour, StorageWaitTimeout: time.Minute}
}
func ledgerBytes(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	var files, reserved int64
	err := s.DB.QueryRow(context.Background(), `SELECT COALESCE((SELECT sum(size_bytes) FROM storage_files),0),COALESCE((SELECT sum(size_bytes) FROM storage_reservations),0)`).Scan(&files, &reserved)
	if err != nil {
		t.Fatal(err)
	}
	return files, reserved
}
func writeLedgerFile(t *testing.T, c Config, kind, name string, bytes int) string {
	t.Helper()
	path := filepath.Join(c.DataDir, kind, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, bytes), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestDurableSourceAdmissionSerializesIndependentAPIs(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 6 << 20
	// Independent API instances use one shared database, no Go mutex in common.
	other := &Store{DB: s.DB}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			results <- store.ReserveSource(context.Background(), c, []string{"one", "two"}[i])
		}(i, store)
	}
	wg.Wait()
	close(results)
	accepted, denied := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, errSourceStorage) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || denied != 1 {
		t.Fatalf("overcommitted durable admission:accepted=%d denied=%d", accepted, denied)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 4<<20 {
		t.Fatal("reservation not persisted", reserved)
	}
}
func TestPreparationTokenFencesStaleCompletionAndRelease(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	if err := s.ReserveSource(ctx, c, "owner", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE storage_reservations SET token='new'"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSource(ctx, "owner", "old"); err != nil {
		t.Fatal(err)
	}
	path := writeLedgerFile(t, c, "sources", "stale", 8)
	err := s.AddSource(ctx, Source{ID: newID("src"), Owner: "owner", StorageToken: "old", Kind: "upload", Path: path, DurationMS: 10000})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("stale source producer overwrote a newer reservation", err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 4<<20 {
		t.Fatal("stale release consumed newer reservation")
	}
}
func TestWaitingStorageResumesAndDeadlineIsFinite(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxOutputBytes = 12 << 20
	c.MaxStorageBytes = 15 << 20
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	job, err := s.CreateJobLimited(ctx, "owner", requestFor(source), "", 3, 32)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveSource(ctx, c, "other"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal("job claimed without space", err)
	}
	waiting, err := s.Job(ctx, job.ID, job.Owner)
	if err != nil || waiting.Status != "waiting_storage" || waiting.StorageWaitUntil == nil {
		t.Fatal("storage wait became terminal", waiting, err)
	}
	if _, err = s.CreateJobLimited(ctx, "owner", requestFor(source), "", 1, 32); !errors.Is(err, ErrBusy) {
		t.Fatal("storage wait bypassed queue quota", err)
	}
	if err = s.ReleaseSource(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	claimed, token, err := s.Claim(ctx)
	if err != nil || claimed.ID != job.ID {
		t.Fatal("waiter did not resume", err)
	}
	if err = s.WaitForStorage(ctx, job.ID, token, c); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE jobs SET storage_wait_until=now()-interval '1 second' WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	expired, err := s.Job(ctx, job.ID, job.Owner)
	if err != nil || expired.Status != "failed" || expired.Items[0].ErrorCode != "storage_timeout" {
		t.Fatal("wait deadline not durable", expired, err)
	}
}
func TestQueueFairnessRotatesOwners(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	a, b := storedSource(t, s, "a"), storedSource(t, s, "b")
	for _, v := range []Source{a, a, b} {
		if _, err := s.CreateJob(ctx, v.Owner, requestFor(v), ""); err != nil {
			t.Fatal(err)
		}
	}
	first, token, err := s.Claim(ctx)
	if err != nil || first.Owner != "a" {
		t.Fatal(err)
	}
	first.Status = "succeeded"
	if err = s.SaveJob(ctx, first, token); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseJobStorage(ctx, first.ID, token); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.Claim(ctx)
	if err != nil || next.Owner != "b" {
		t.Fatal("older owner queue monopolized worker", next.Owner, err)
	}
}
func TestArtifactPublicationConvertsReservationAtomically(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	sourceFile, err := os.Stat(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes, initialReserved := ledgerBytes(t, s)
	if sourceFile.Size() <= 0 || sourceBytes != sourceFile.Size() || initialReserved != 0 {
		t.Fatal("completed source is not charged exactly once", sourceBytes, sourceFile.Size(), initialReserved)
	}
	req := requestFor(source)
	req.Ranges = append(req.Ranges, req.Ranges[0])
	if _, err := s.CreateJob(ctx, source.Owner, req, ""); err != nil {
		t.Fatal(err)
	}
	j, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := writeLedgerFile(t, c, "artifacts", "result.mp4", 4096)
	a := Artifact{ID: newID("art"), Filename: "result.mp4", SizeBytes: 4096, ActualStartMS: 1000, ActualEndMS: 3000}
	if err = s.PublishArtifact(ctx, publicationSnapshot(j, a), path, token, a); err != nil {
		t.Fatal(err)
	}
	files, reserved := ledgerBytes(t, s)
	if files-sourceBytes != 4096 || reserved != c.MaxOutputBytes {
		t.Fatal("publication lost or double-counted storage", sourceBytes, files, reserved)
	}
	// The expiry is hidden while its parent job pins the result.
	loaded, err := s.Job(ctx, j.ID, j.Owner)
	if err != nil || loaded.Items[0].Artifact == nil || loaded.Items[0].Artifact.ExpiresAt != nil {
		t.Fatal(err)
	}
	loaded.Status = "succeeded"
	if err = s.SaveJob(ctx, loaded, token); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.Job(ctx, j.ID, j.Owner)
	if err != nil || loaded.Items[0].Artifact.ExpiresAt == nil {
		t.Fatal("published result has no retention deadline", err)
	}
}
func TestTombstoneChargesFailedDeleteUntilPhysicalRemoval(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	path := writeLedgerFile(t, c, "sources", "source", 23)
	source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: path, DurationMS: 10000}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	paths, err := s.DeleteSource(ctx, source.ID, source.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.DrainStorageDeletes(ctx, c, paths); err == nil {
		t.Fatal("unsafe directory removal accepted")
	}
	bytes, _ := ledgerBytes(t, s)
	if bytes != 23 {
		t.Fatal("failed delete freed budget", bytes)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE storage_files SET delete_retry_at=clock_timestamp() WHERE path=$1", path); err != nil {
		t.Fatal(err)
	}
	if err = s.DrainStorageDeletes(ctx, c, paths); err != nil {
		t.Fatal(err)
	}
	bytes, _ = ledgerBytes(t, s)
	if bytes != 0 {
		t.Fatal("successful retry did not release tombstone")
	}
}

func TestStorageDeleteBatchesRetainUnsafeFilesAndReleaseSuccessfulFiles(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	paths := make([]string, storageBatch*2+3)
	for i := range paths {
		paths[i] = filepath.Join(c.DataDir, fmt.Sprintf("missing-%d", i))
	}
	// Failed physical deletion must not hold up acknowledgement of the other
	// successful files, including batches larger than the maintenance limit.
	if err := os.Mkdir(paths[0], 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,delete_pending) SELECT path,'tombstone',17,true FROM unnest($1::text[]) AS f(path)`, paths); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainStorageDeletes(ctx, c, paths); err == nil {
		t.Fatal("non-regular media path accepted")
	}
	stored, reserved := ledgerBytes(t, s)
	if stored != 17 || reserved != 0 {
		t.Fatal("successful removals stayed charged or unsafe deletion freed bytes", stored, reserved)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatal("unsafe directory was removed", err)
	}
}

func TestStorageDeleteAcknowledgementFailureKeepsBytesForRetry(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	path := writeLedgerFile(t, c, "sources", "removed-before-commit", 19)
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,delete_pending) VALUES($1,'tombstone',19,true)`, path); err != nil {
		t.Fatal(err)
	}
	ops := systemStorageDeleteOperations()
	ops.commit = func(context.Context, pgx.Tx) error { return io.ErrUnexpectedEOF }
	err := s.drainStorageDeletes(ctx, c, []string{path}, ops)
	if !errors.Is(err, io.ErrUnexpectedEOF) || storageDeletionRetryOnly(err) {
		t.Fatal("lost acknowledgement must stop the database batch", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("test did not reach the physical removal before acknowledgement", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 19 {
		t.Fatal("failed acknowledgement uncharged tombstone", stored)
	}
	if err := s.DrainStorageDeletes(ctx, c, []string{path}); err != nil {
		t.Fatal("retry did not acknowledge already missing file", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 0 {
		t.Fatal("retry left removed file charged", stored)
	}
}

func TestStorageTombstoneBatchDeduplicatesPathsAndPreservesLargestSize(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	missing := filepath.Join(c.DataDir, "missing")
	actual := writeLedgerFile(t, c, "sources", "actual", 13)
	tx, err := s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	files := []storageDeletionFile{{path: missing, owner: "owner", size: 2}, {path: missing, owner: "owner", size: 7}, {path: missing, owner: "owner", size: 5}, {path: actual, owner: "owner", size: 100}, {}}
	if err = tombstoneStorageFiles(ctx, tx, files); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 20 {
		t.Fatal("batch changed physical/fallback size accounting", stored)
	}
}

func TestStorageReconciliationSkipsUnchangedOrphansAndTracksGrowth(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	path := writeLedgerFile(t, c, "sources", "orphan", 13)
	if err := s.ReconcileStorage(ctx, c); err != nil {
		t.Fatal(err)
	}
	version := func() string {
		t.Helper()
		var value string
		if err := s.DB.QueryRow(ctx, "SELECT xmin::text FROM storage_files WHERE path=$1", path).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := version()
	if err := s.ReconcileStorage(ctx, c); err != nil {
		t.Fatal(err)
	}
	if version() != before {
		t.Fatal("unchanged orphan rewrote its database tuple")
	}
	if err := os.WriteFile(path, make([]byte, 29), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileStorage(ctx, c); err != nil {
		t.Fatal(err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 29 || version() == before {
		t.Fatal("growing orphan did not update its charged bytes", stored)
	}
}

func TestStorageReconciliationFindsNewOrphansBehindPendingBatch(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,delete_pending,observed_at) SELECT 'pending-'||i,'orphan',17,true,now()-interval '3 days' FROM generate_series(1,$1) i`, storageBatch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,observed_at) VALUES('newly-expired','orphan',19,now()-interval '2 days')`); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileStorage(ctx, c); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := s.DB.QueryRow(ctx, "SELECT delete_pending FROM storage_files WHERE path='newly-expired'").Scan(&pending); err != nil || !pending {
		t.Fatal("old pending tombstones hid newly expired orphan", err)
	}
}
func TestSourceDeletionProtectsWaitersAndOwnership(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	job, err := s.CreateJob(ctx, source.Owner, requestFor(source), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE jobs SET status='waiting_storage' WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteSource(ctx, source.ID, "foreign"); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign source deletion leak", err)
	}
	if _, err = s.DeleteSource(ctx, source.ID, source.Owner); !errors.Is(err, ErrSourceInUse) {
		t.Fatal("deleted pinned source", err)
	}
	if err = s.Cancel(ctx, job.ID, job.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteSource(ctx, source.ID, source.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Job(ctx, job.ID, job.Owner); !errors.Is(err, ErrNotFound) {
		t.Fatal("explicit delete left terminal jobs", err)
	}
}
func TestReservationMathAndTotalDurationRejectOverflow(t *testing.T) {
	c := Config{MaxOutputBytes: math.MaxInt64, MaxSourceBytes: math.MaxInt64}
	if _, err := remainingJobReserve(c, Job{Items: make([]JobItem, 2)}, false); err == nil {
		t.Fatal("output reservation wrapped")
	}
	if _, err := remainingJobReserve(c, Job{Items: make([]JobItem, 1)}, true); err == nil {
		t.Fatal("HLS reservation wrapped")
	}
	req := ExportRequest{Ranges: []Range{{StartMS: 0, EndMS: math.MaxInt64 - 1}, {StartMS: 0, EndMS: 2}}, Format: "mp4", Quality: "best", CutMode: "accurate"}
	c.MaxRanges, c.MaxRangeMS, c.MaxJobMS = 2, math.MaxInt64, math.MaxInt64
	if err := req.Validate(c, Source{DurationMS: math.MaxInt64}); err == nil {
		t.Fatal("total duration wrapped")
	}
}
func TestBootstrapAdoptsLegacyFilesWithoutDeletingFreshOrphans(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	known := writeLedgerFile(t, c, "sources", "legacy.mp4", 100)
	fresh := writeLedgerFile(t, c, "artifacts", "fresh.mp4", 50)
	source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: known, DurationMS: 10000}
	if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,path) VALUES($1,$2,'legacy',10000,'upload',$3)`, source.ID, source.Owner, known); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveSource(ctx, c, "new"); err != nil {
		t.Fatal(err)
	}
	files, _ := ledgerBytes(t, s)
	if files != 150 {
		t.Fatal("bootstrap missed existing files", files)
	}
	if err := s.ReconcileStorage(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh orphan was deleted", err)
	}
	var pending bool
	if err := s.DB.QueryRow(ctx, "SELECT delete_pending FROM storage_files WHERE path=$1", fresh).Scan(&pending); err != nil || pending {
		t.Fatal("fresh orphan queued for deletion", err)
	}
}

func TestReclaimChargesBothLeaseWorkspacesUntilCleanup(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	j, oldToken, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(c.DataDir, "work", j.ID+"-"+oldToken)
	if err = os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(oldDir, "partial"), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1", j.ID); err != nil {
		t.Fatal(err)
	}
	recovered, newToken, err := s.Claim(ctx)
	if err != nil || newToken == oldToken || recovered.ID != j.ID {
		t.Fatal(err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 2*c.MaxOutputBytes {
		t.Fatal("reclaim uncharged old workspace", reserved)
	}
	if err = s.ReleaseJobStorage(ctx, j.ID, oldToken); err == nil {
		t.Fatal("release freed a retained old workspace")
	}
	if err = removeStorageTree(ctx, oldDir); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseJobStorage(ctx, j.ID, oldToken); err != nil {
		t.Fatal(err)
	}
	// Releasing one token never removes the new worker's independent reservation.
	_, reserved = ledgerBytes(t, s)
	if reserved != c.MaxOutputBytes {
		t.Fatal("stale release removed new lease budget")
	}
}

func TestMaintenanceRemovesExpiredLeaseWorkspaceAndReleasesItsBudget(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	j, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(c.DataDir, "work", j.ID+"-"+token)
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "partial"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE jobs SET lease_until=now()-interval '2 minutes' WHERE id=$1", j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE storage_reservations SET abandoned_at=now()-interval '2 minutes' WHERE job_id=$1", j.ID); err != nil {
		t.Fatal(err)
	}
	if err = RunMaintenance(ctx, c, s); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(work); !os.IsNotExist(err) {
		t.Fatal("abandoned workspace remained", err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 0 {
		t.Fatal("removed workspace still consumes reservation", reserved)
	}
}

func TestSourceListIsBoundedAndPrivate(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	for i := 0; i < 23; i++ {
		storedSource(t, s, "owner")
	}
	storedSource(t, s, "foreign")
	sources, err := s.Sources(ctx, "owner")
	if err != nil || len(sources) != 20 {
		t.Fatal("list was not bounded", len(sources), err)
	}
	for _, v := range sources {
		if v.Owner != "owner" || v.ExpiresAt == nil {
			t.Fatal("source list disclosed foreign or incomplete retention info")
		}
	}
	empty, err := s.Sources(ctx, "empty")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("empty list should encode as []", err)
	}
}

func TestDeleteAndCreateJobRaceNeverLeavesDanglingSource(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		source := storedSource(t, s, "owner")
		var created Job
		var createErr, deleteErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			created, createErr = s.CreateJobLimited(ctx, source.Owner, requestFor(source), "", 32, 64)
		}()
		go func() { defer wg.Done(); _, deleteErr = s.DeleteSource(ctx, source.ID, source.Owner) }()
		wg.Wait()
		if createErr == nil {
			if !errors.Is(deleteErr, ErrSourceInUse) {
				t.Fatal("source deleted after concurrent job admission", deleteErr)
			}
			if err := s.Cancel(ctx, created.ID, created.Owner); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteSource(ctx, source.ID, source.Owner); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(createErr, ErrNotFound) || deleteErr != nil {
			t.Fatal("race leaked internal error", createErr, deleteErr)
		}
	}
}

func TestOversizedJobRejectedBeforeEnteringWaitQueue(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 3 << 20
	s.ConfigureStorage(c)
	source := storedSource(t, s, "owner")
	if _, err := s.CreateJobLimited(context.Background(), source.Owner, requestFor(source), "", 3, 32); !errors.Is(err, ErrJobTooLarge) {
		t.Fatal("impossible export entered waiting queue", err)
	}
}

func TestWaitingStorageHTTPExposesFiniteDeadlineAndSourceListIsolation(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxOutputBytes = 12 << 20
	c.MaxStorageBytes = 15 << 20
	c.MutationsPerMinute = 20
	server := NewServer(c, s)
	ctx := context.Background()
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	identity := httptest.NewRequest("GET", "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	source := storedSource(t, s, owner)
	j, err := s.CreateJobLimited(ctx, owner, requestFor(source), "", 3, 32)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveSource(ctx, c, "other"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/jobs/"+j.ID, nil)
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	var job Job
	if err = json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || job.Status != "waiting_storage" || job.StorageWaitUntil == nil || !job.StorageWaitUntil.After(time.Now()) || job.StorageWaitUntil.After(time.Now().Add(c.StorageWaitTimeout+time.Second)) {
		t.Fatal("HTTP deadline contract missing", response.Code, response.Body.String())
	}
	foreignBytes := make([]byte, 32)
	foreignBytes[0] = 1
	foreign := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(foreignBytes)}
	req = httptest.NewRequest("GET", "/api/v1/sources", nil)
	req.AddCookie(foreign)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	var list struct{ Sources []Source }
	if err = json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || list.Sources == nil || len(list.Sources) != 0 {
		t.Fatal("foreign source list leaked ownership", response.Body.String())
	}
	req = httptest.NewRequest("DELETE", "/api/v1/sources/"+source.ID, nil)
	req.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != 409 {
		t.Fatal("HTTP deletion allowed active storage waiter", response.Code)
	}
}
func TestWorkerStorageFailureWaitsInsteadOfFailingAcceptedJob(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	c.FFmpeg = filepath.Join(c.DataDir, "ffmpeg-no-space")
	if err := os.WriteFile(c.FFmpeg, []byte("#!/bin/sh\nprintf 'No space left on device\\n' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	j, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	processJob(ctx, c, s, j, token)
	after, err := s.Job(ctx, j.ID, j.Owner)
	if err != nil || after.Status != "waiting_storage" || after.StorageWaitUntil == nil {
		t.Fatal("ENOSPC became terminal", after, err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 0 {
		t.Fatal("removed failed workspace still reserved bytes")
	}
}
func TestFailedPreparationAdoptsSurvivingFileBeforeReleasingReservation(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	id := newID("src")
	if err := s.ReserveSource(ctx, c, "owner", "token"); err != nil {
		t.Fatal(err)
	}
	writeLedgerFile(t, c, "sources", id+".media", 37)
	if err := s.FinishSource(ctx, c, "owner", "token", id); err != nil {
		t.Fatal(err)
	}
	files, reserved := ledgerBytes(t, s)
	if files != 37 || reserved != 0 {
		t.Fatal("failed preparation freed unaccounted surviving bytes", files, reserved)
	}
	var accounted, reservation int64
	if err := s.DB.QueryRow(ctx, "SELECT stored_bytes::bigint,reserved_bytes::bigint FROM storage_counters").Scan(&accounted, &reservation); err != nil || accounted != files || reservation != reserved {
		t.Fatal("transactional counters diverged", err)
	}
}

func TestPlatformMetadataPreparationReservesOnlyThumbnailBudget(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxOwnerBytes = 10 << 20
	s.ConfigureStorage(c)
	ctx := context.Background()
	path := writeLedgerFile(t, c, "sources", "existing", 7<<20)
	source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: path, DurationMS: 10000}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveSource(ctx, c, source.Owner, "upload", false); !errors.Is(err, errSourceStorage) {
		t.Fatal("upload bypassed retained source budget", err)
	}
	if err := s.reserveSource(ctx, c, source.Owner, "platform", true); err != nil {
		t.Fatal("metadata-only platform blocked by upload byte ceiling", err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != maxThumbnailBytes {
		t.Fatal("platform unnecessarily reserved a full source download", reserved)
	}
}

func TestCompletedPlatformRecoveryNeedsNoMediaAccessOrOutputReservation(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := Source{ID: newID("src"), Owner: "owner", Kind: "platform", Title: "Completed", DurationMS: 10000, URL: "https://www.twitch.tv/videos/123"}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	j, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := writeLedgerFile(t, c, "artifacts", "complete.mp4", 64)
	a := Artifact{ID: newID("art"), Filename: "complete.mp4", SizeBytes: 64}
	if err = s.PublishArtifact(ctx, publicationSnapshot(j, a), path, token, a); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1", j.ID); err != nil {
		t.Fatal(err)
	}
	recovered, newToken, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 0 {
		t.Fatal("completed platform job still reserved HLS/download space", reserved)
	}
	c.YTDLP, c.FFmpeg, c.FFprobe = "/does-not-exist", "/does-not-exist", "/does-not-exist"
	processJob(ctx, c, s, recovered, newToken)
	finished, err := s.Job(ctx, j.ID, j.Owner)
	if err != nil || finished.Status != "succeeded" {
		t.Fatal("completed recovery performed unavailable media inspection", finished, err)
	}
}

func TestBlockedMaintenanceReleasesAdmissionLockPromptly(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	if err := s.ReserveSource(ctx, c, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSource(ctx, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	source := storedSource(t, s, "owner")
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	lock, relation := blockArtifactCleanup(t, s)
	done := make(chan error, 1)
	go func() {
		bounded, cancel := context.WithTimeout(ctx, workerMaintenanceTimeout)
		defer cancel()
		_, err := s.Cleanup(bounded, c.ArtifactTTL, c.SourceTTL)
		done <- err
	}()
	waitForCleanupLock(t, lock, relation)
	started := time.Now()
	claimCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, _, err := s.Claim(claimCtx); err != nil {
		t.Fatal("blocked cleanup stopped durable claims", err)
	}
	if time.Since(started) > 1500*time.Millisecond {
		t.Fatal("maintenance held admission lock too long")
	}
	if err := <-done; err == nil {
		t.Fatal("blocked cleanup should fail and retry later")
	}
}
