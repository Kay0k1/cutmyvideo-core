package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	q.Set("pool_max_conns", "2")
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
	if _, err := s.DB.Exec(ctx, `UPDATE sources SET thumbnail_path='/test/expired-thumbnail' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
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
	if len(paths) != 3 {
		t.Fatalf("expected source, thumbnail, and artifact cleanup, got %v", paths)
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
	cookieA := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	otherBytes := make([]byte, 32)
	otherBytes[0] = 1
	cookieB := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(otherBytes)}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookieA)
	owner, _ := ownerFromRequest(request)
	source := storedSource(t, s, owner)
	path := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE sources SET path=$1 WHERE id=$2`, path, source.ID); err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(ctx, owner, requestFor(source), "")
	if err != nil {
		t.Fatal(err)
	}
	artifactID := newID("art")
	_, err = s.DB.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms) VALUES($1,$2,$3,$4,'test.mp4',10,0,1000)`, artifactID, owner, j.ID, path)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(Config{MutationsPerMinute: 200}, s).Handler()
	for _, route := range []string{"/api/v1/sources/" + source.ID, "/api/v1/sources/" + source.ID + "/media", "/api/v1/jobs/" + j.ID, "/api/v1/artifacts/" + artifactID + "/download"} {
		for _, tc := range []struct {
			cookie *http.Cookie
			status int
		}{{nil, 401}, {cookieA, 200}, {cookieB, 404}} {
			r := httptest.NewRequest("GET", route, nil)
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%s got %d wanted %d", route, w.Code, tc.status)
			}
		}
		if strings.HasSuffix(route, "/media") || strings.HasSuffix(route, "/download") {
			r := httptest.NewRequest("GET", route, nil)
			r.AddCookie(cookieA)
			r.Header.Set("Range", "bytes=0-3")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 206 || w.Body.String() != "0123" {
				t.Fatalf("authorized range response: %d %s", w.Code, w.Body.String())
			}
		}
	}
}

func TestStoreConcurrentAdmissionUsesOneConnectionPerTransaction(t *testing.T) {
	s := testStore(t)
	source := storedSource(t, s, "owner")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CreateJobLimited(ctx, "owner", requestFor(source), "req-"+strconv.Itoa(i), 12, 32)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 10 {
		t.Fatalf("concurrent admission count %d: %v", count, err)
	}
}

func TestRunningCancellationAndExhaustedRecoveryHaveConsistentItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	_, err := s.CreateJob(ctx, "owner", requestFor(source), "")
	if err != nil {
		t.Fatal(err)
	}
	j, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	j.Items[0].Status = "running"
	if err = s.SaveJob(ctx, j, token); err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel(ctx, j.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveJob(ctx, j, token); err != nil {
		t.Fatal(err)
	}
	current, err := s.Job(ctx, j.ID, "owner")
	if err != nil || current.Status != "running" {
		t.Fatal("cancellation became terminal before worker completion")
	}
	j.Status = "cancelled"
	j.Items[0].Status = "cancelled"
	if err = s.SaveJob(ctx, j, token); err != nil {
		t.Fatal(err)
	}
	current, err = s.Job(ctx, j.ID, "owner")
	if err != nil || current.Status != "cancelled" || current.Items[0].Status != "cancelled" {
		t.Fatal("inconsistent running cancellation")
	}
	if err = s.SaveJob(ctx, j, token); !errors.Is(err, ErrNotFound) {
		t.Fatal("terminal job accepted another worker write")
	}
	_, err = s.CreateJob(ctx, "owner", requestFor(source), "")
	if err != nil {
		t.Fatal(err)
	}
	j, token, err = s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `UPDATE jobs SET attempts=3,lease_until=now()-interval '1 second' WHERE id=$1`, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	current, err = s.Job(ctx, j.ID, "owner")
	if err != nil || current.Status != "failed" || current.Items[0].Status != "failed" {
		t.Fatalf("inconsistent recovery: %+v %v", current, err)
	}
}

func TestHTTPThumbnailPresentationAndIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookie)
	owner, _ := ownerFromRequest(request)
	path := filepath.Join(t.TempDir(), "source.thumbnail")
	thumb := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var content bytes.Buffer
	if err := png.Encode(&content, thumb); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	v := Source{ID: newID("src"), Owner: owner, Title: "Public recording", Kind: "platform", Provider: "twitch", ProviderID: "v123", URL: "https://www.twitch.tv/videos/123", DurationMS: 10000, ThumbnailPath: path}
	if err := s.AddSource(ctx, v); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Source(ctx, v.ID, owner)
	if err != nil || loaded.Provider != "twitch" || loaded.PreviewKind != "none" || loaded.ProviderVideoID == nil || *loaded.ProviderVideoID != "v123" || loaded.SourceURL == nil || *loaded.SourceURL != v.URL || loaded.ThumbnailURL == nil {
		t.Fatalf("metadata was not persisted/presented: %+v %v", loaded, err)
	}
	other := make([]byte, 32)
	other[0] = 1
	foreign := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(other)}
	handler := NewServer(Config{}, s).Handler()
	for _, tt := range []struct {
		cookie *http.Cookie
		code   int
	}{{nil, 401}, {foreign, 404}, {cookie, 200}} {
		req := httptest.NewRequest("GET", *loaded.ThumbnailURL, nil)
		if tt.cookie != nil {
			req.AddCookie(tt.cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tt.code {
			t.Fatalf("thumbnail status%d want%d", w.Code, tt.code)
		}
		if tt.code == 200 && (w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), content.Bytes())) {
			t.Fatal("thumbnail MIME/content changed")
		}
	}
	if _, err := s.DB.Exec(ctx, `UPDATE sources SET created_at=now()-interval '2 days' WHERE id=$1`, v.ID); err != nil {
		t.Fatal(err)
	}
	paths, err := s.Cleanup(ctx, time.Hour, time.Hour)
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatalf("thumbnail retention failed:%v %v", paths, err)
	}
}
