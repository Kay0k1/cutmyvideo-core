package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/jackc/pgx/v5"
)

func posterFixture(t *testing.T) (*Store, *Server, *http.Cookie, Source, Artifact) {
	t.Helper()
	s := testStore(t)
	c := agentRecoveryConfig(t)
	c.MaxStorageBytes = 256 << 20
	c.MaxOwnerBytes = 256 << 20
	server := NewServer(c, s)
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookie)
	owner, _ := ownerFromRequest(request)
	for _, dir := range []string{"sources", "artifacts", "work"} {
		if err := os.MkdirAll(filepath.Join(c.DataDir, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	input := filepath.Join(c.DataDir, "sources", "picture.mp4")
	mcRun(t, context.Background(), c, "-f", "lavfi", "-i", "color=red:size=160x96:rate=25:duration=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", input)
	source, err := InspectLocal(context.Background(), c, input)
	if err != nil {
		t.Fatal(err)
	}
	source.ID, source.Owner, source.Title = newID("src"), owner, "Poster parent"
	if err = s.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateJob(context.Background(), owner, requestFor(source), "poster-fixture")
	if err != nil {
		t.Fatal(err)
	}
	claimed, token, err := s.Claim(context.Background())
	if err != nil || claimed.ID != job.ID {
		t.Fatal(err)
	}
	processJob(context.Background(), c, s, claimed, token)
	job, err = s.Job(context.Background(), job.ID, owner)
	if err != nil || job.Status != "succeeded" || job.Items[0].Artifact == nil {
		t.Fatal("verified parent export", job.Status, err)
	}
	return s, server, cookie, source, *job.Items[0].Artifact
}

func posterHTTP(t *testing.T, server *Server, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, databaseRequest(method, path, body, cookie))
	return response
}

func TestPosterHTTPArtifactLifecycleAndOwnership(t *testing.T) {
	s, server, cookie, source, parent := posterFixture(t)
	url := "/api/v1/artifacts/" + parent.ID + "/poster"
	r := posterHTTP(t, server, cookie, "POST", url, `{"timestamp_ms":500,"format":"png"}`)
	if r.Code != 201 {
		t.Fatalf("poster status %d %s", r.Code, r.Body.String())
	}
	var a ImageArtifact
	if err := json.Unmarshal(r.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.ID == parent.ID || a.ParentArtifactID != parent.ID || a.Width != 160 || a.Height != 96 || a.TimestampMS != 500 || a.Format != "png" || a.ExpiresAt == nil || parent.ExpiresAt == nil || a.ExpiresAt.After(*parent.ExpiresAt) {
		t.Fatalf("image metadata %+v parent %+v", a, parent)
	}
	r = posterHTTP(t, server, cookie, "GET", a.DownloadURL, "")
	if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" {
		t.Fatal("image download", r.Code)
	}
	imageBytes := append([]byte(nil), r.Body.Bytes()...)
	decoded, format, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil || format != "png" || decoded.Bounds().Dx() != 160 {
		t.Fatal("actual PNG decode", format, err)
	}
	r = posterHTTP(t, server, cookie, "POST", url, `{"timestamp_ms":500,"format":"png"}`)
	var repeated ImageArtifact
	_ = json.Unmarshal(r.Body.Bytes(), &repeated)
	if r.Code != 200 || repeated.ID != a.ID {
		t.Fatal("same request created another image", r.Code, repeated.ID)
	}
	var count int
	if err = s.DB.QueryRow(context.Background(), "SELECT count(*) FROM image_artifacts WHERE parent_artifact_id=$1", parent.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("image reuse", count, err)
	}
	storageState := func() [3]int64 {
		t.Helper()
		var state [3]int64
		if err := s.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM storage_files),(SELECT count(*) FROM storage_reservations),(SELECT count(*) FROM artifact_readers)`).Scan(&state[0], &state[1], &state[2]); err != nil {
			t.Fatal(err)
		}
		return state
	}
	beforeForeign := storageState()
	foreign := *cookie
	foreign.Value = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	for _, tc := range []struct{ method, path, body string }{{"POST", url, `{"timestamp_ms":500}`}, {"GET", a.DownloadURL, ""}, {"GET", "/api/v1/image-artifacts/" + a.ID, ""}, {"DELETE", "/api/v1/image-artifacts/" + a.ID, ""}} {
		if got := posterHTTP(t, server, &foreign, tc.method, tc.path, tc.body); got.Code != 404 {
			t.Fatal("foreign image access", tc.method, got.Code)
		}
	}
	if after := storageState(); after != beforeForeign {
		t.Fatal("foreign poster requests changed durable storage state", beforeForeign, after)
	}
	for _, body := range []string{`{}`, `null`, `{"timestamp_ms":-1}`, `{"timestamp_ms":2000}`, `{"timestamp_ms":0,"format":"svg"}`, `{"timestamp_ms":0,"unexpected":true}`} {
		if got := posterHTTP(t, server, cookie, "POST", url, body); got.Code != 400 {
			t.Fatal("invalid poster accepted", body, got.Code)
		}
	}
	server.Config.MaxOwnerBytes = 1
	s.ConfigureStorage(server.Config)
	beforeQuota := storageState()
	r = posterHTTP(t, server, cookie, "POST", url, `{"timestamp_ms":700}`)
	if r.Code != 507 {
		t.Fatal("owner quota ignored", r.Code)
	}
	if after := storageState(); after != beforeQuota {
		t.Fatal("known poster quota refusal changed durable storage state", beforeQuota, after)
	}
	// Existing completed images remain recoverable before tightened quota checks.
	r = posterHTTP(t, server, cookie, "POST", url, `{"timestamp_ms":500,"format":"png"}`)
	if r.Code != 200 {
		t.Fatal("retained image was blocked by new quota", r.Code)
	}
	r = posterHTTP(t, server, cookie, "DELETE", "/api/v1/image-artifacts/"+a.ID, "")
	if r.Code != 204 {
		t.Fatal("image deletion", r.Code)
	}
	if got := posterHTTP(t, server, cookie, "GET", a.DownloadURL, ""); got.Code != 404 {
		t.Fatal("deleted image remains available")
	}
	server.Config.MaxOwnerBytes = 256 << 20
	s.ConfigureStorage(server.Config)
	r = posterHTTP(t, server, cookie, "POST", url, `{"timestamp_ms":0}`)
	if r.Code != 201 {
		t.Fatal("default JPEG", r.Code, r.Body.String())
	}
	_ = json.Unmarshal(r.Body.Bytes(), &a)
	paths, err := s.DeleteSource(context.Background(), source.ID, source.Owner)
	if err != nil {
		t.Fatal("parent lifecycle deletion", err)
	}
	if err = s.DrainStorageDeletes(context.Background(), server.Config, paths); err != nil {
		t.Fatal(err)
	}
	if got := posterHTTP(t, server, cookie, "GET", a.DownloadURL, ""); got.Code != 404 {
		t.Fatal("image survived parent deletion")
	}
	if err = s.DB.QueryRow(context.Background(), "SELECT count(*) FROM image_artifacts").Scan(&count); err != nil || count != 0 {
		t.Fatal("cascaded image metadata remains", count, err)
	}
}

func TestPosterParentReaderFencesAndPublication(t *testing.T) {
	s, server, _, source, parent := posterFixture(t)
	ctx := context.Background()
	o := localmedia.PosterOptions{TimestampMS: 0, Format: "jpeg"}
	lease, existing, err := s.acquirePoster(ctx, server.Config, source.Owner, parent.ID, newID("img"), o)
	if err != nil || existing != nil {
		t.Fatal("parent reader admission", err)
	}
	if _, err = s.DeleteSource(ctx, source.ID, source.Owner); !errors.Is(err, ErrSourceInUse) {
		t.Fatal("active poster did not protect parent source deletion", err)
	}
	if _, err = s.DB.Exec(ctx, "DELETE FROM artifacts WHERE id=$1", parent.ID); err == nil {
		t.Fatal("direct parent deletion bypassed reader FK")
	}
	if _, _, err = s.acquirePoster(ctx, server.Config, source.Owner, parent.ID, newID("img"), o); !errors.Is(err, errSourceServerBusy) {
		t.Fatal("poster decoder admission is unbounded", err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE artifacts SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cleanup(ctx, time.Hour, time.Hour); err != nil {
		t.Fatal("retention failed on pinned parent", err)
	}
	if _, _, err = s.ArtifactPath(ctx, parent.ID, source.Owner); err != nil {
		t.Fatal("retention removed active poster input", err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE artifacts SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1", parent.ID); err != nil {
		t.Fatal(err)
	}
	dir := posterWorkspace(server.Config, lease.id)
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := localmedia.GeneratePoster(ctx, server.Config, lease.path, filepath.Join(dir, "ready"), o)
	if err != nil {
		t.Fatal(err)
	}
	final := posterPath(server.Config, lease.id, lease.format)
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(final, data, 0600); err != nil {
		t.Fatal(err)
	}
	result.Path = final
	_, err = s.publishPosterWithCommit(ctx, server.Config, lease, result, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return errors.New("synthetic lost commit acknowledgement")
	})
	if !errors.Is(err, errPosterCommitUncertain) {
		t.Fatal("lost acknowledgement was treated as proven rollback", err)
	}
	a, _, err := s.imageArtifact(ctx, lease.id, source.Owner)
	if err != nil {
		t.Fatal("committed image was not recoverable after lost acknowledgement", err)
	}
	if err = s.releasePoster(ctx, server.Config, lease); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(final); err != nil || !bytes.Equal(after, data) {
		t.Fatal("lease cleanup removed published image", err)
	}
	// A real PostgreSQL COMMIT rejection leaves the final bytes charged until
	// the deletion worker confirms removal; it must not resemble a lost reply.
	rejected, _, err := s.acquirePoster(ctx, server.Config, source.Owner, parent.ID, newID("img"), localmedia.PosterOptions{TimestampMS: 20, Format: "jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	rejectedPath := posterPath(server.Config, rejected.id, rejected.format)
	if err = os.WriteFile(rejectedPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	rejectedResult := result
	rejectedResult.Path, rejectedResult.TimestampMS = rejectedPath, 20
	migrationExec(t, s.DB, `CREATE FUNCTION reject_poster_test_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic constraint rejection' USING ERRCODE='23514'; END $$;
CREATE CONSTRAINT TRIGGER reject_poster_test_commit AFTER INSERT ON image_artifacts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_poster_test_commit();`)
	_, err = s.publishPoster(ctx, server.Config, rejected, rejectedResult)
	if err == nil || errors.Is(err, errPosterCommitUncertain) || !publicationCommitRejected(err) {
		t.Fatal("real constraint rejection classified incorrectly", err)
	}
	migrationExec(t, s.DB, "DROP TRIGGER reject_poster_test_commit ON image_artifacts; DROP FUNCTION reject_poster_test_commit()")
	if err = s.releasePoster(ctx, server.Config, rejected); err != nil {
		t.Fatal(err)
	}
	var charged int64
	var pending bool
	if err = s.DB.QueryRow(ctx, "SELECT size_bytes,delete_pending FROM storage_files WHERE path=$1", rejectedPath).Scan(&charged, &pending); err != nil || charged != int64(len(data)) || !pending {
		t.Fatal("rejected publication dropped its unresolved byte charge", charged, pending, err)
	}
	if err = s.DrainStorageDeletes(ctx, server.Config, []string{rejectedPath}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(rejectedPath); !os.IsNotExist(err) {
		t.Fatal("rejected image leaked", err)
	}
	// A crash leaves a reservation plus private workspace. Expiry adopts any
	// final file into the charged deletion ledger before releasing the reserve.
	crashed, _, err := s.acquirePoster(ctx, server.Config, source.Owner, parent.ID, newID("img"), localmedia.PosterOptions{TimestampMS: 10, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	crashPath := posterPath(server.Config, crashed.id, crashed.format)
	if err = os.WriteFile(crashPath, []byte("unpublished crash image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE storage_reservations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", "poster:"+crashed.id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE artifact_readers SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", crashed.id); err != nil {
		t.Fatal(err)
	}
	paths, err := s.Cleanup(ctx, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DrainStorageDeletes(ctx, server.Config, paths); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(crashPath); !os.IsNotExist(err) {
		t.Fatal("crashed image leaked", err)
	}
	if _, _, err = s.imageArtifact(ctx, a.ID, source.Owner); err != nil {
		t.Fatal("crash cleanup removed unrelated image", err)
	}
	// Client cancellation stops the whole process group and retires the reader.
	marker := filepath.Join(server.Config.DataDir, "poster-running")
	script := filepath.Join(server.Config.DataDir, "poster-ffmpeg")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	if err = os.WriteFile(script, []byte("#!/bin/sh\n: > "+quoted+"\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server.Config.FFmpeg = script
	request := databaseRequest("POST", "/api/v1/artifacts/"+parent.ID+"/poster", `{"timestamp_ms":100,"format":"png"}`, &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(httptest.NewRecorder(), request.WithContext(cancelCtx))
		close(done)
	}()
	waitForWorkerFile(t, marker)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("poster cancellation did not stop process")
	}
	var leases int
	if err = s.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM artifact_readers)+(SELECT count(*) FROM storage_reservations WHERE kind='poster')").Scan(&leases); err != nil || leases != 0 {
		t.Fatal("cancelled poster retained reader/reservation", leases, err)
	}
}
