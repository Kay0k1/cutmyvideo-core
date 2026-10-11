package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestRetentionPreservesExplicitExpiryAndAllPins(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"expired-free", "live-free", "old-preview", "old-artifacts", "old-queued", "old-running", "old-waiting", "old-terminal"} {
		path := writeLedgerFile(t, c, "sources", id, 7)
		if err := s.AddSource(ctx, Source{ID: id, Owner: "owner", Kind: "upload", Path: path, DurationMS: 10000}); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE sources SET created_at=now()-interval '2 hours' WHERE id<>'live-free'")
	thumb := writeLedgerFile(t, c, "sources", "expired-thumbnail", 3)
	exec("UPDATE sources SET thumbnail_path=$1 WHERE id='expired-free'", thumb)
	exec("INSERT INTO storage_files(path,owner,resource_id,kind,size_bytes) VALUES($1,'owner','expired-free','thumbnail',3)", thumb)
	exec(`INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id)
 VALUES('preview-pin','owner','preview',16,now()+interval '1 hour','old-preview')`)
	for _, job := range []struct {
		id, source, status string
		old                bool
	}{
		{"recent-artifacts", "old-artifacts", "succeeded", false},
		{"active-queued", "old-queued", "queued", true},
		{"active-running", "old-running", "running", true},
		{"active-waiting", "old-waiting", "waiting_storage", true},
		{"terminal-artifact", "old-terminal", "failed", true},
		{"terminal-empty", "live-free", "cancelled", true},
	} {
		exec(`INSERT INTO jobs(id,owner,source_id,request,items,status,updated_at)
 VALUES($1,'owner',$2,'{}','[]',$3,now()-CASE WHEN $4 THEN interval '2 hours' ELSE interval '0 seconds' END)`, job.id, job.source, job.status, job.old)
	}
	var removed []string
	for _, artifact := range []struct {
		id, job     string
		age, expiry *time.Duration
	}{
		{"explicit-expired-fresh", "recent-artifacts", retentionDuration(0), retentionDuration(-time.Hour)},
		{"explicit-future-old", "recent-artifacts", retentionDuration(2 * time.Hour), retentionDuration(time.Hour)},
		{"legacy-expired-old", "recent-artifacts", retentionDuration(2 * time.Hour), nil},
		{"legacy-live-recent", "recent-artifacts", retentionDuration(0), nil},
		{"pinned-queued-expired", "active-queued", retentionDuration(2 * time.Hour), retentionDuration(-time.Hour)},
		{"pinned-running-legacy", "active-running", retentionDuration(2 * time.Hour), nil},
		{"pinned-waiting-expired", "active-waiting", retentionDuration(2 * time.Hour), retentionDuration(-time.Hour)},
		{"terminal-future-artifact", "terminal-artifact", retentionDuration(2 * time.Hour), retentionDuration(time.Hour)},
	} {
		path := writeLedgerFile(t, c, "artifacts", artifact.id, 16)
		var expiry any
		if artifact.expiry != nil {
			expiry = artifact.expiry.Seconds()
		}
		// Direct insertion represents historical nullable expiry records; current
		// publication normally records expires_at explicitly.
		exec(`INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,created_at,expires_at)
 VALUES($1,'owner',$2,$3,'clip.mp4',16,0,1000,now()-($4*interval '1 second'),now()+($5::double precision*interval '1 second'))`, artifact.id, artifact.job, path, artifact.age.Seconds(), expiry)
		exec("INSERT INTO storage_files(path,owner,resource_id,kind,size_bytes) VALUES($1,'owner',$2,'artifact',16)", path, artifact.id)
		if artifact.id == "explicit-expired-fresh" || artifact.id == "legacy-expired-old" {
			removed = append(removed, path)
		}
	}
	removed = append(removed, filepath.Join(c.DataDir, "sources", "expired-free"), thumb)
	initialBytes, initialReserved := ledgerBytes(t, s)
	batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	slices.Sort(batch.paths)
	if batch.full || !slices.Equal(batch.paths, removed) {
		t.Fatalf("wrong retention tombstones: %+v, want %v", batch, removed)
	}
	for _, path := range removed {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("metadata cleanup prematurely removed physical bytes", err)
		}
	}
	if files, reserved := ledgerBytes(t, s); files != initialBytes || reserved != initialReserved {
		t.Fatalf("retention prematurely released charge: %d/%d, want %d/%d", files, reserved, initialBytes, initialReserved)
	}
	for _, expectation := range []struct {
		table, id string
		exists    bool
	}{
		{"sources", "expired-free", false}, {"sources", "live-free", true}, {"sources", "old-preview", true},
		{"sources", "old-artifacts", true}, {"sources", "old-queued", true}, {"sources", "old-running", true}, {"sources", "old-waiting", true}, {"sources", "old-terminal", true},
		{"jobs", "terminal-empty", false}, {"jobs", "terminal-artifact", true}, {"jobs", "recent-artifacts", true},
		{"jobs", "active-queued", true}, {"jobs", "active-running", true}, {"jobs", "active-waiting", true},
		{"artifacts", "explicit-expired-fresh", false}, {"artifacts", "legacy-expired-old", false},
		{"artifacts", "explicit-future-old", true}, {"artifacts", "legacy-live-recent", true}, {"artifacts", "pinned-queued-expired", true},
		{"artifacts", "pinned-running-legacy", true}, {"artifacts", "pinned-waiting-expired", true}, {"artifacts", "terminal-future-artifact", true},
	} {
		var exists bool
		if err := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+expectation.table+" WHERE id=$1)", expectation.id).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != expectation.exists {
			t.Errorf("%s %s exists=%t, want %t", expectation.table, expectation.id, exists, expectation.exists)
		}
	}
}

func retentionDuration(value time.Duration) *time.Duration { return &value }

func TestRetentionBatchesInterleaveLegacyAndExplicitExpiry(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	if _, err := s.DB.Exec(ctx, `INSERT INTO jobs(id,owner,source_id,request,items,status) VALUES('retained-job','owner',$1,'{}','[]','succeeded')`, source.ID); err != nil {
		t.Fatal(err)
	}
	count := storageBatch*2 + 1
	for i := 1; i <= count; i++ {
		writeLedgerFile(t, c, "artifacts", fmt.Sprintf("artifact-%04d", i), 16)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,created_at,expires_at)
 SELECT 'artifact-'||lpad(i::text,4,'0'),'owner','retained-job',$2||'/artifact-'||lpad(i::text,4,'0'),'clip.mp4',16,0,1000,
 now()-interval '2 hours'+i*interval '1 second',CASE WHEN i=$1 THEN now()+interval '1 hour' WHEN i%2=0 THEN NULL ELSE now()-interval '1 hour' END
 FROM generate_series(1,$1) i`, count, filepath.Join(c.DataDir, "artifacts")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,owner,resource_id,kind,size_bytes) SELECT path,owner,id,'artifact',size_bytes FROM artifacts`); err != nil {
		t.Fatal(err)
	}
	initialBytes, _ := ledgerBytes(t, s)
	for batchNumber := 1; batchNumber <= 2; batchNumber++ {
		batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if !batch.full {
			t.Fatal("full mixed-expiry batch did not request continued maintenance")
		}
		var remaining, tombstones, oldestRemaining int
		if err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM artifacts),(SELECT count(*) FROM storage_files WHERE delete_pending),
 (SELECT count(*) FROM artifacts WHERE id<=$1)`, fmt.Sprintf("artifact-%04d", batchNumber*storageBatch)).Scan(&remaining, &tombstones, &oldestRemaining); err != nil {
			t.Fatal(err)
		}
		if remaining != count-batchNumber*storageBatch || tombstones != batchNumber*storageBatch || oldestRemaining != 0 {
			t.Fatalf("batch %d did not remove exactly the globally oldest %d legacy/explicit-expiry records: remaining=%d tombstones=%d oldest=%d", batchNumber, storageBatch, remaining, tombstones, oldestRemaining)
		}
		if files, _ := ledgerBytes(t, s); files != initialBytes {
			t.Fatal("batch retention released bytes before physical deletion", files, initialBytes)
		}
	}
	var futureRetained bool
	if err := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM artifacts WHERE id=$1)", fmt.Sprintf("artifact-%04d", count)).Scan(&futureRetained); err != nil || !futureRetained {
		t.Fatal("explicit future expiry did not override old creation time", err)
	}
	for i := 1; i <= count; i++ {
		if _, err := os.Stat(filepath.Join(c.DataDir, "artifacts", fmt.Sprintf("artifact-%04d", i))); err != nil {
			t.Fatal("retention removed physical file before drain", err)
		}
	}
}

func TestRetentionLimitsJobsAndMetadataOnlySourcesIndependently(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	count := storageBatch*2 + 1
	if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,url,created_at)
 SELECT 'old-source-'||lpad(i::text,4,'0'),'owner','historical platform metadata',10000,'platform','https://www.youtube.com/watch?v=fixture',now()-interval '2 hours'+i*interval '1 second'
 FROM generate_series(1,$1) i`, count); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,url) VALUES('live-parent','owner','platform metadata',10000,'platform','https://www.youtube.com/watch?v=live')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO jobs(id,owner,source_id,request,items,status,updated_at)
 SELECT 'old-job-'||lpad(i::text,4,'0'),'owner','live-parent','{}','[]','succeeded',now()-interval '2 hours'+i*interval '1 second' FROM generate_series(1,$1) i`, count); err != nil {
		t.Fatal(err)
	}
	for batchNumber := 1; batchNumber <= 3; batchNumber++ {
		batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		wantRemaining := max(0, count-batchNumber*storageBatch)
		var sources, jobs int
		if err = s.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM sources WHERE id<>'live-parent'),(SELECT count(*) FROM jobs)").Scan(&sources, &jobs); err != nil {
			t.Fatal(err)
		}
		if sources != wantRemaining || jobs != wantRemaining || batch.full != (batchNumber < 3) || len(batch.paths) != 0 {
			t.Fatalf("independent metadata-only batches: cycle=%d sources=%d jobs=%d batch=%+v", batchNumber, sources, jobs, batch)
		}
	}
}
