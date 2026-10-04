package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func waitForWorkerFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not reach the media process")
}

func TestWorkerShutdownKeepsJobRecoverableAndResumesActualExport(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout, c.SourceTimeout = 100<<20, 20*time.Second, time.Second
	c.WorkerHealthPath = filepath.Join(c.DataDir, "health")
	if err := os.MkdirAll(filepath.Join(c.DataDir, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	path := filepath.Join(c.DataDir, "source.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=800:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", path); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: newID("src"), Owner: "owner", Title: "Resume", Kind: "upload", Path: path, DurationMS: 4000}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(ctx, "owner", requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(c.DataDir, "media-started")
	blockedFFmpeg := filepath.Join(c.DataDir, "blocked-ffmpeg")
	// Generated paths are quoted independently; no source content is executed.
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	if err := os.WriteFile(blockedFFmpeg, []byte("#!/bin/sh\n: > "+quoted+"\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	blocked := c
	blocked.FFmpeg = blockedFFmpeg
	parent, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { processJob(parent, blocked, s, job, token); close(done) }()
	waitForWorkerFile(t, marker)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not stop process group")
	}
	interrupted, err := s.Job(ctx, job.ID, "owner")
	if err != nil || interrupted.Status != "running" || interrupted.Items[0].Status != "running" || interrupted.Items[0].ErrorCode != "" {
		t.Fatalf("shutdown became a terminal failure: %+v %v", interrupted, err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	resumed, newToken, err := s.Claim(ctx)
	if err != nil || resumed.ID != job.ID || newToken == token {
		t.Fatal("interrupted job could not be reclaimed", err)
	}
	if err := s.SaveJob(ctx, interrupted, token); !errors.Is(err, ErrNotFound) {
		t.Fatal("old worker could still update resumed job", err)
	}
	processJob(ctx, c, s, resumed, newToken)
	finished, err := s.Job(ctx, job.ID, "owner")
	if err != nil || finished.Status != "succeeded" || finished.Items[0].Artifact == nil {
		t.Fatalf("resumed real export failed: %+v %v", finished, err)
	}
	artifact := finished.Items[0].Artifact
	if artifact.ActualStartMS != 1000 || artifact.ActualEndMS < 2950 || artifact.ActualEndMS > 3100 {
		t.Fatalf("resumed range changed: %+v", artifact)
	}
	output, _, err := s.ArtifactPath(ctx, artifact.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := probe(ctx, c, output, false)
	if err != nil || len(p.Streams) != 2 {
		t.Fatalf("resumed artifact lost media: %+v %v", p, err)
	}
}

func TestWorkerOwnDeadlineIsTerminalAndStoresSafeCode(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout = 100<<20, 150*time.Millisecond
	c.WorkerHealthPath = filepath.Join(c.DataDir, "health")
	c.FFmpeg = filepath.Join(c.DataDir, "blocked-ffmpeg")
	if err := os.WriteFile(c.FFmpeg, []byte("#!/bin/sh\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	if _, err := s.CreateJob(ctx, "owner", requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	processJob(ctx, c, s, job, token)
	finished, err := s.Job(ctx, job.ID, "owner")
	if err != nil || finished.Status != "failed" || finished.Items[0].ErrorCode != "job_timeout" {
		t.Fatalf("job deadline did not settle: %+v %v", finished, err)
	}
}

func TestWorkerUserCancellationStopsProcessAndStoresCancelledItem(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout = 100<<20, 20*time.Second
	c.WorkerHealthPath = filepath.Join(c.DataDir, "health")
	marker := filepath.Join(c.DataDir, "started")
	c.FFmpeg = filepath.Join(c.DataDir, "blocked-ffmpeg")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	if err := os.WriteFile(c.FFmpeg, []byte("#!/bin/sh\n: > "+quoted+"\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := storedSource(t, s, "owner")
	if _, err := s.CreateJob(ctx, "owner", requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { processJob(ctx, c, s, job, token); close(done) }()
	waitForWorkerFile(t, marker)
	if err := s.Cancel(ctx, job.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("user cancellation did not stop media process")
	}
	finished, err := s.Job(ctx, job.ID, "owner")
	if err != nil || finished.Status != "cancelled" || finished.Items[0].Status != "cancelled" || finished.Items[0].ErrorCode != "cancelled" || finished.Items[0].Artifact != nil {
		t.Fatalf("cancellation became a timeout or result: %+v %v", finished, err)
	}
}

func TestWorkerShutdownDuringPublicationKeepsLeaseRecoverable(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout = 100<<20, 20*time.Second
	c.WorkerHealthPath = filepath.Join(c.DataDir, "health")
	if err := os.MkdirAll(filepath.Join(c.DataDir, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	path := filepath.Join(c.DataDir, "source.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=800:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", path); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: newID("src"), Owner: "owner", Title: "Publication resume", Kind: "upload", Path: path, DurationMS: 4000}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	request := requestFor(source)
	request.Ranges = append([]Range{{StartMS: 0, EndMS: 1000, Label: "Already ready"}}, request.Ranges...)
	if _, err := s.CreateJob(ctx, "owner", request, ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstID := newID("art")
	firstPath := filepath.Join(c.DataDir, "artifacts", firstID+".mp4")
	start, end, err := exportMedia(ctx, c, []string{path}, false, request.Ranges[0], request, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	first := Artifact{ID: firstID, Filename: "ready.mp4", DownloadURL: "/ready", ActualStartMS: start, ActualEndMS: end}
	if err := s.AddArtifact(ctx, "owner", job.ID, firstPath, token, first); err != nil {
		t.Fatal(err)
	}
	job.Items[0].Status, job.Items[0].Artifact = "succeeded", &first
	if err := s.SaveJob(ctx, job, token); err != nil {
		t.Fatal(err)
	}
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, "LOCK TABLE artifacts IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	var relation uint32
	if err := lock.QueryRow(ctx, "SELECT 'artifacts'::regclass::oid").Scan(&relation); err != nil {
		t.Fatal(err)
	}
	observer, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	parent, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { processJob(parent, c, s, job, token); close(done) }()
	// Wait for the database lock itself: cancellation is precisely inside
	// artifact registration, after export and the publishing state write.
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation=$1 AND mode='RowExclusiveLock' AND NOT granted)`, relation).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("worker did not enter artifact publication barrier")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel blocked publication")
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	interrupted, err := s.Job(ctx, job.ID, "owner")
	if err != nil || interrupted.Status != "running" || interrupted.Stage != "publishing" || interrupted.Items[1].Status != "running" || interrupted.Items[1].ErrorCode != "" || interrupted.Items[0].Artifact == nil || interrupted.Items[0].Artifact.ID != firstID {
		t.Fatalf("publication shutdown became terminal: %+v %v", interrupted, err)
	}
	files, err := os.ReadDir(filepath.Join(c.DataDir, "artifacts"))
	if err != nil || len(files) != 1 || files[0].Name() != firstID+".mp4" {
		t.Fatal("unregistered publication artifact was retained", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	resumed, newToken, err := s.Claim(ctx)
	if err != nil || resumed.ID != job.ID {
		t.Fatal("publication could not be reclaimed", err)
	}
	processJob(ctx, c, s, resumed, newToken)
	finished, err := s.Job(ctx, job.ID, "owner")
	if err != nil || finished.Status != "succeeded" || finished.Items[1].Artifact == nil || finished.Items[0].Artifact == nil || finished.Items[0].Artifact.ID != firstID {
		t.Fatalf("publication did not resume: %+v %v", finished, err)
	}
}

func TestRunWorkerHealthCoversIdleBusyAndCancellation(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout = 100<<20, 20*time.Second
	c.WorkerHealthPath = filepath.Join(c.DataDir, "health")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() { workerDone <- RunWorker(ctx, c, s) }()
	waitForWorkerFile(t, c.WorkerHealthPath)
	deadline := time.Now().Add(5 * time.Second)
	for CheckWorkerHealth(c.WorkerHealthPath) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := CheckWorkerHealth(c.WorkerHealthPath); err != nil {
		t.Fatal("idle queue did not update health", err)
	}
	marker := filepath.Join(c.DataDir, "media-started")
	// Start a separately claimed long process to exercise the busy heartbeat
	// against the same isolated store and this worker's local marker.
	blocked := c
	blocked.FFmpeg = filepath.Join(c.DataDir, "blocked-ffmpeg")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	if err := os.WriteFile(blocked.FFmpeg, []byte("#!/bin/sh\n: > "+quoted+"\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
	if CheckWorkerHealth(c.WorkerHealthPath) == nil {
		t.Fatal("cancelled idle worker remained healthy")
	}
	source := storedSource(t, s, "owner")
	if _, err := s.CreateJob(context.Background(), "owner", requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	busyCtx, busyCancel := context.WithCancel(context.Background())
	defer busyCancel()
	go func() { workerDone <- RunWorker(busyCtx, blocked, s) }()
	waitForWorkerFile(t, marker)
	stale := time.Now().Add(-time.Minute)
	if err := os.Chtimes(c.WorkerHealthPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	if CheckWorkerHealth(c.WorkerHealthPath) == nil {
		t.Fatal("stale busy marker appeared healthy")
	}
	deadline = time.Now().Add(5 * time.Second)
	for CheckWorkerHealth(c.WorkerHealthPath) != nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := CheckWorkerHealth(c.WorkerHealthPath); err != nil {
		t.Fatal("busy lease heartbeat did not update health", err)
	}
	busyCancel()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("busy worker did not stop")
	}
	if CheckWorkerHealth(c.WorkerHealthPath) == nil {
		t.Fatal("stopped busy worker remained healthy")
	}
}
