package app

import (
	"context"
	"errors"
	"fmt"
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

const cleanupLockWaitSQL = `SELECT COALESCE(min(mode),'') FROM pg_locks WHERE locktype='relation' AND relation=$1 AND NOT granted AND pid<>pg_backend_pid()`

func waitForCleanupLock(t *testing.T, tx pgx.Tx, relation uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var mode string
		// A materialized retention selector may read the relation before the
		// DELETE requests its write lock. Observe the actual blocked relation
		// access, rather than depending on that query's planning order.
		if err := tx.QueryRow(ctx, cleanupLockWaitSQL, relation).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != "" {
			t.Logf("maintenance reached cleanup barrier: mode=%s", mode)
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
	for _, stopBy := range []string{"deadline", "cancel"} {
		t.Run(stopBy, func(t *testing.T) {
			s := testStore(t)
			c := maintenanceConfig(t)
			old := time.Now().Add(-3 * time.Hour)
			write := func(kind, name string) string {
				t.Helper()
				path := filepath.Join(c.DataDir, kind, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
				return path
			}
			known := write("sources", "active.mp4")
			source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: known, DurationMS: 10000}
			if err := s.AddSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateJob(context.Background(), source.Owner, requestFor(source), ""); err != nil {
				t.Fatal(err)
			}
			job, token, err := s.Claim(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			artifactPath := write("artifacts", "active.mp4")
			artifact := Artifact{ID: newID("art"), Filename: "active.mp4", SizeBytes: 5}
			if err := s.AddArtifact(context.Background(), source.Owner, job.ID, artifactPath, token, artifact); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(context.Background(), "UPDATE sources SET created_at=$2 WHERE id=$1", source.ID, old); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(context.Background(), "UPDATE artifacts SET created_at=$2,expires_at=$2 WHERE id=$1", artifact.ID, old); err != nil {
				t.Fatal(err)
			}
			// Remaining stages may legitimately clean an expired orphan after the
			// retention stage times out. Active data must remain pinned throughout.
			write("sources", "orphan.mp4")
			tx, relation := blockArtifactCleanup(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			started := time.Now()
			done := make(chan error, 1)
			go func() { done <- cleanupFiles(ctx, c, s) }()
			waitForCleanupLock(t, tx, relation)
			want := error(context.DeadlineExceeded)
			if stopBy == "cancel" {
				want = context.Canceled
				cancel()
			}
			select {
			case err := <-done:
				t.Logf("blocked cleanup stop=%s elapsed=%s error=%v", stopBy, time.Since(started), err)
				if !errors.Is(err, want) {
					t.Fatal("blocked maintenance did not report its deadline/cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked maintenance exceeded its cancellation/deadline budget")
			}
			probe, probeCancel := context.WithTimeout(context.Background(), time.Second)
			defer probeCancel()
			for {
				var mode string
				if err := tx.QueryRow(probe, cleanupLockWaitSQL, relation).Scan(&mode); err != nil {
					t.Fatal("blocked cleanup retained a relation waiter", err)
				}
				if mode == "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			var pinned bool
			var accounted int
			if err := tx.QueryRow(probe, `SELECT EXISTS(SELECT 1 FROM sources WHERE id=$1) AND EXISTS(SELECT 1 FROM artifacts WHERE id=$2) AND EXISTS(SELECT 1 FROM jobs WHERE id=$3 AND status='running'),(SELECT count(*) FROM storage_files WHERE path=ANY($4) AND NOT delete_pending)`, source.ID, artifact.ID, job.ID, []string{known, artifactPath}).Scan(&pinned, &accounted); err != nil || !pinned || accounted != 2 {
				t.Fatal("bounded maintenance changed active metadata or accounting", pinned, accounted, err)
			}
			for _, path := range []string{known, artifactPath} {
				if data, err := os.ReadFile(path); err != nil || string(data) != "media" {
					t.Fatal("bounded maintenance removed or changed active media", path, err)
				}
			}
		})
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

func TestCleanupFilesDrainsBacklogInBoundedBatches(t *testing.T) {
	for _, fixture := range []struct {
		name          string
		count         int
		files         bool
		blockedDelete bool
		remaining     int
	}{
		{name: "media", count: storageBatch*2 + 7, files: true},
		{name: "metadata_only", count: storageBatch*2 + 7},
		{name: "eight_batch_limit", count: storageBatch*9 + 1, remaining: storageBatch + 1},
		{name: "deletion_error", count: storageBatch*2 + 7, blockedDelete: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := testStore(t)
			c := maintenanceConfig(t)
			s.ConfigureStorage(c)
			ctx := context.Background()
			paths := make([]string, fixture.count)
			for i := range paths {
				if fixture.files {
					paths[i] = writeLedgerFile(t, c, "sources", fmt.Sprintf("expired-%d", i), 13)
				}
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,path,created_at) SELECT 'expired-'||ordinal,'owner','Expired',10000,'upload',path,now()-interval '2 hours' FROM unnest($1::text[]) WITH ORDINALITY AS f(path,ordinal)`, paths); err != nil {
				t.Fatal(err)
			}
			if fixture.blockedDelete {
				blocked := filepath.Join(c.DataDir, "sources", "blocked")
				if err := os.MkdirAll(blocked, 0700); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,delete_pending) VALUES($1,'tombstone',17,true)`, blocked); err != nil {
					t.Fatal(err)
				}
			}
			// A queued job must pin its old source while several other batches
			// expire in the same maintenance cycle.
			active := storedSource(t, s, "active")
			if _, err := s.DB.Exec(ctx, "UPDATE sources SET created_at=now()-interval '2 hours' WHERE id=$1", active.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateJob(ctx, active.Owner, requestFor(active), ""); err != nil {
				t.Fatal(err)
			}
			bounded, cancel := context.WithTimeout(ctx, workerMaintenanceTimeout)
			err := cleanupFiles(bounded, c, s)
			cancel()
			if (err != nil) != fixture.blockedDelete {
				t.Fatal("unexpected cleanup result", err)
			}
			var remaining int
			if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM sources WHERE owner='owner'").Scan(&remaining); err != nil || remaining != fixture.remaining {
				t.Fatal("cleanup did not drain the bounded backlog", remaining, err)
			}
			if _, err := s.Source(ctx, active.ID, active.Owner); err != nil {
				t.Fatal("backlog cleanup removed active source", err)
			}
			if fixture.files {
				for _, path := range paths {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("expired media remains after metadata cleanup", err)
					}
				}
			}
		})
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
