package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestWorkerDatabaseOperationsBoundPoolWait(t *testing.T) {
	s := testStore(t)
	for i := int32(0); i < s.DB.Config().MaxConns; i++ {
		conn, err := s.DB.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conn.Release)
	}
	for name, operation := range map[string]func() error{
		"claim":     func() error { _, _, err := claimWorkerJob(context.Background(), s); return err },
		"recover":   func() error { return recoverWorkerJobs(context.Background(), s) },
		"heartbeat": func() error { _, err := heartbeatWorkerJob(context.Background(), s, "job", "lease"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- operation() }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("unavailable pool must return its bounded deadline", err)
				}
			case <-time.After(workerDatabaseTimeout + 2*time.Second):
				t.Fatal("database operation exceeded its worker deadline")
			}
		})
	}
}

func blockArtifactCleanup(t *testing.T, s *Store) (pgx.Tx, uint32) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, s.DB.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, "LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	var relation uint32
	if err := tx.QueryRow(ctx, "SELECT 'artifacts'::regclass::oid").Scan(&relation); err != nil {
		t.Fatal(err)
	}
	return tx, relation
}

func waitForCleanupLock(t *testing.T, tx pgx.Tx, relation uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var waiting bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation=$1 AND mode='RowExclusiveLock' AND NOT granted)`, relation).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("maintenance did not reach its database cleanup barrier")
}

func maintenanceConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{DataDir: dir, WorkerHealthPath: filepath.Join(dir, "worker-health"),
		SourceTTL: time.Hour, ArtifactTTL: time.Hour, JobTimeout: time.Minute, SourceTimeout: time.Second}
}

func TestMaintenanceDoesNotBlockQueueAndStopsOnShutdown(t *testing.T) {
	s := testStore(t)
	tx, relation := blockArtifactCleanup(t, s)
	c := maintenanceConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var workerErr error
	go func() {
		workerErr = RunWorker(ctx, c, s)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("worker did not stop during test cleanup")
		}
	})
	waitForCleanupLock(t, tx, relation)
	deadline := time.Now().Add(3 * time.Second)
	for CheckWorkerHealth(c.WorkerHealthPath) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := CheckWorkerHealth(c.WorkerHealthPath); err != nil {
		t.Fatal("blocked maintenance prevented a successful idle queue heartbeat", err)
	}
	cancel()
	select {
	case <-done:
		if workerErr != nil {
			t.Fatal(workerErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel and await blocked maintenance")
	}
	if _, err := os.Stat(c.WorkerHealthPath); !os.IsNotExist(err) {
		t.Fatal("stopped worker retained its health marker", err)
	}
}

func TestMaintenanceDoesNotRefreshWorkerHealth(t *testing.T) {
	s := testStore(t)
	tx, relation := blockArtifactCleanup(t, s)
	c := maintenanceConfig(t)
	health, err := newWorkerHealth(c.WorkerHealthPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(health.Close)
	before, err := os.Stat(c.WorkerHealthPath)
	if err != nil {
		t.Fatal(err)
	}
	stop := startWorkerMaintenance(context.Background(), c, s)
	t.Cleanup(stop)
	waitForCleanupLock(t, tx, relation)
	stop()
	after, err := os.Stat(c.WorkerHealthPath)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || CheckWorkerHealth(c.WorkerHealthPath) == nil {
		t.Fatal("maintenance disguised a stale queue/processing heartbeat", err)
	}
}

func TestMaintenanceCycleStopsBlockedCleanupAtDeadline(t *testing.T) {
	s := testStore(t)
	tx, relation := blockArtifactCleanup(t, s)
	c := maintenanceConfig(t)
	orphan := filepath.Join(c.DataDir, "sources", "orphan.mp4")
	if err := os.MkdirAll(filepath.Dir(orphan), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	stop := startWorkerMaintenance(context.Background(), c, s)
	t.Cleanup(stop)
	waitForCleanupLock(t, tx, relation)
	ctx, cancel := context.WithTimeout(context.Background(), workerMaintenanceTimeout+2*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation=$1 AND mode='RowExclusiveLock' AND NOT granted)`, relation).Scan(&waiting); err != nil {
			t.Fatal("blocked cleanup did not leave the database at its deadline", err)
		}
		if !waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("expired maintenance continued orphan deletion after its deadline", err)
	}
}

func TestCleanupFilesPreservesActiveDataAndOrphanSafetyFloor(t *testing.T) {
	s := testStore(t)
	c := maintenanceConfig(t)
	c.SourceTTL, c.ArtifactTTL, c.JobTimeout, c.SourceTimeout = 5*time.Minute, 5*time.Minute, 10*time.Minute, time.Minute
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-10 * time.Minute)
	write := func(kind, name string, age time.Time) string {
		path := filepath.Join(c.DataDir, kind, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, age, age); err != nil {
			t.Fatal(err)
		}
		return path
	}
	known := write("sources", "known.mp4", old)
	source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: known, DurationMS: 10000}
	ctx := context.Background()
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET created_at=now()-interval '2 hours' WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	artifact := write("artifacts", "known.mp4", old)
	if err := s.AddArtifact(ctx, source.Owner, job.ID, artifact, token, Artifact{ID: newID("art"), Filename: "known.mp4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE artifacts SET created_at=now()-interval '2 hours' WHERE job_id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	preserved := []string{known, artifact, write("sources", "recent.mp4", recent), write("artifacts", "recent.mp4", recent)}
	removed := []string{write("sources", "orphan.mp4", old), write("artifacts", "orphan.mp4", old)}
	for _, pair := range []struct {
		name string
		age  time.Time
	}{{"abandoned", old}, {"recent", recent}} {
		path := filepath.Join(c.DataDir, "work", pair.name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "segment"), []byte("media"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, pair.age, pair.age); err != nil {
			t.Fatal(err)
		}
		if pair.name == "abandoned" {
			removed = append(removed, path)
		} else {
			preserved = append(preserved, path)
		}
	}
	cleanupFiles(ctx, c, s)
	for _, path := range preserved {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("active data or young orphan was removed", filepath.Base(path), err)
		}
	}
	for _, path := range removed {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("expired orphan was not removed", filepath.Base(path), err)
		}
	}
}

func TestStorageAdmissionHandlesBudgetOverflowAndCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "media"), make([]byte, 64), 0600); err != nil {
		t.Fatal(err)
	}
	c := Config{DataDir: dir, MaxStorageBytes: 128}
	if !storageAvailable(c, 64) || storageAvailable(c, 65) || storageAvailable(c, -1) {
		t.Fatal("logical storage budget does not include its reservation")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if storageAvailableContext(cancelled, c, 0) {
		t.Fatal("cancelled admission continued filesystem work")
	}
	c.DataDir = filepath.Join(dir, "not-created", "data")
	if !storageAvailable(c, 64) {
		t.Fatal("first admission must work before the data directory exists")
	}
}
