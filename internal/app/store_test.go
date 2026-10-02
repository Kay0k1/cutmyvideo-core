package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := newID("test")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := OpenStore(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.DB.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return s
}

func storedSource(t *testing.T, s *Store, owner string) Source {
	t.Helper()
	v := Source{ID: newID("src"), Owner: owner, Title: "Test", Kind: "upload", DurationMS: 10000, Path: "/test/" + newID("file")}
	if err := s.AddSource(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return v
}

func requestFor(v Source) ExportRequest {
	return ExportRequest{SourceID: v.ID, Ranges: []Range{{StartMS: 1000, EndMS: 3000, Label: "Test"}}, Format: "mp4", Quality: "720p", CutMode: "accurate"}
}

func TestStoreConcurrentClaimsFencingAndRecovery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	jobs := map[string]bool{}
	for i := 0; i < 2; i++ {
		j, err := s.CreateJob(ctx, "owner", requestFor(source), "")
		if err != nil {
			t.Fatal(err)
		}
		jobs[j.ID] = true
	}
	type claim struct {
		job   Job
		token string
		err   error
	}
	results := make(chan claim, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); j, token, err := s.Claim(ctx); results <- claim{j, token, err} }()
	}
	wg.Wait()
	close(results)
	claimed := map[string]string{}
	for c := range results {
		if errors.Is(c.err, ErrNotFound) {
			continue
		}
		if c.err != nil {
			t.Fatal(c.err)
		}
		if !jobs[c.job.ID] || claimed[c.job.ID] != "" {
			t.Fatal("duplicate or unknown claim")
		}
		claimed[c.job.ID] = c.token
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d jobs", len(claimed))
	}
	var expired, oldToken string
	for id, token := range claimed {
		expired, oldToken = id, token
		break
	}
	if _, err := s.DB.Exec(ctx, `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, expired); err != nil {
		t.Fatal(err)
	}
	j, newToken, err := s.Claim(ctx)
	if err != nil || j.ID != expired || newToken == oldToken {
		t.Fatalf("recovery failed %+v %v", j, err)
	}
	if err = s.SaveJob(ctx, j, oldToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale worker wrote state: %v", err)
	}
	artifact := Artifact{ID: newID("art"), Filename: "test.mp4", SizeBytes: 10, ActualStartMS: 1000, ActualEndMS: 3000}
	if err = s.AddArtifact(ctx, "owner", j.ID, "/test/result", oldToken, artifact); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale worker published artifact: %v", err)
	}
	if err = s.AddArtifact(ctx, "owner", j.ID, "/test/result", newToken, artifact); err != nil {
		t.Fatal(err)
	}
}

func TestStoreIdempotencyOwnerLimitsAndCancellation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	j, err := s.CreateJobLimited(ctx, "owner", requestFor(source), "retry-key", 1, 32)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.CreateJobLimited(ctx, "owner", requestFor(source), "retry-key", 1, 32)
	if err != nil || replayed.ID != j.ID {
		t.Fatal("idempotent retry created a second job")
	}
	if _, err = s.CreateJobLimited(ctx, "owner", requestFor(source), "another", 1, 32); !errors.Is(err, ErrBusy) {
		t.Fatalf("owner cap: %v", err)
	}
	if err = s.Cancel(ctx, j.ID, "other-owner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session cancellation: %v", err)
	}
	if err = s.Cancel(ctx, j.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	j, err = s.Job(ctx, j.ID, "owner")
	if err != nil || j.Status != "cancelled" || j.Items[0].Status != "cancelled" {
		t.Fatalf("inconsistent queued cancellation: %+v %v", j, err)
	}
	if _, _, err = s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal("claimed a cancelled job")
	}
}

func TestStoreCleanupExpiresTerminalDataAndPinsActiveSources(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := storedSource(t, s, "old-owner")
	active := storedSource(t, s, "active-owner")
	oldJob, err := s.CreateJob(ctx, old.Owner, requestFor(old), "")
	if err != nil {
		t.Fatal(err)
	}
	activeJob, err := s.CreateJob(ctx, active.Owner, requestFor(active), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE sources SET created_at=now()-interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE jobs SET status='succeeded',updated_at=now()-interval '2 days' WHERE id=$1`, oldJob.ID); err != nil {
		t.Fatal(err)
	}
	for _, j := range []Job{oldJob, activeJob} {
		_, err = s.DB.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,created_at) VALUES($1,$2,$3,$4,'test.mp4',10,0,1000,now()-interval '2 days')`, newID("art"), j.Owner, j.ID, "/test/"+j.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	paths, err := s.Cleanup(ctx, 24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected source and artifact cleanup, got %v", paths)
	}
	if _, err = s.Source(ctx, old.ID, old.Owner); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired source remains")
	}
	if _, err = s.Source(ctx, active.ID, active.Owner); err != nil {
		t.Fatal("active source was deleted")
	}
	if _, err = s.Job(ctx, oldJob.ID, old.Owner); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired job remains")
	}
}

func TestHTTPResourcesAreSessionIsolated(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	j, err := s.CreateJob(ctx, "owner", requestFor(source), "")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{MutationsPerMinute: 200}, s)
	for _, path := range []string{"/api/v1/sources/" + source.ID, "/api/v1/sources/" + source.ID + "/media", "/api/v1/jobs/" + j.ID, "/api/v1/artifacts/art_missing/download"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		server.withSession(func(w http.ResponseWriter, r *http.Request, _ string) {
			if strings.Contains(path, "/jobs/") {
				server.job(w, r, "other")
			} else if strings.Contains(path, "/artifacts/") {
				server.download(w, r, "other")
			} else {
				server.source(w, r, "other")
			}
		})(w, r)
		if w.Code != 401 {
			t.Fatalf("missing session: %d", w.Code)
		}
	}
	if _, err = s.Source(ctx, source.ID, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal("source ownership bypass")
	}
	if _, err = s.Job(ctx, j.ID, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal("job ownership bypass")
	}
}
