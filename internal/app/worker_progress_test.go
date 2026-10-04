package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerProgressPersistenceFailureKeepsJobRecoverable(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout, c.SourceTimeout = 100<<20, 15*time.Second, time.Second
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(c.DataDir, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(c.DataDir, "source.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-t", "4", "-c:v", "libx264", "-threads", "1", input); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: input, DurationMS: 4000}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A sequence survives rollback: fail precisely the first measured-progress
	// write, then permit reads/writes again. Without the uncertainty flag a
	// successful second write would incorrectly store a terminal timeout.
	_, err = s.DB.Exec(ctx, `CREATE SEQUENCE progress_save_attempt;
CREATE FUNCTION reject_first_progress_save() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE((NEW.items->0->>'progress_ms')::bigint,0)>0 AND nextval('progress_save_attempt')=1 THEN
  RAISE EXCEPTION 'controlled transient progress save failure';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER progress_save_failure BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION reject_first_progress_save();`)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := exec.LookPath(c.FFmpeg)
	if err != nil {
		t.Fatal(err)
	}
	blocked := c
	blocked.FFmpeg = filepath.Join(c.DataDir, "progress-encoder")
	script := "#!/bin/sh\nprintf 'out_time_us=1000000\\n'\nexec " + workerFixtureQuote(actual) + " \"$@\"\n"
	if err := os.WriteFile(blocked.FFmpeg, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	processJob(ctx, blocked, s, job, token)
	interrupted, err := s.Job(ctx, job.ID, source.Owner)
	if err != nil || interrupted.Status != "running" || interrupted.Items[0].Status != "running" || interrupted.Items[0].ErrorCode != "" || interrupted.Items[0].Artifact != nil {
		t.Fatalf("transient progress write became terminal: %+v %v", interrupted, err)
	}
	var attempts int64
	if err := s.DB.QueryRow(ctx, `SELECT last_value FROM progress_save_attempt`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("progress failure did not stop further writes: %d %v", attempts, err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	recovered, newToken, err := s.Claim(ctx)
	if err != nil || recovered.ID != job.ID || newToken == token {
		t.Fatal("interrupted progress job could not be reclaimed", err)
	}
	processJob(ctx, c, s, recovered, newToken)
	finished, err := s.Job(ctx, job.ID, source.Owner)
	if err != nil || finished.Status != "succeeded" || finished.Items[0].Artifact == nil {
		t.Fatalf("progress recovery did not complete: %+v %v", finished, err)
	}
}
