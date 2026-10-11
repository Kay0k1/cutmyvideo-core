package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBatchValidationDefaultsAndCorrectiveRangeErrors(t *testing.T) {
	c := Config{MaxRanges: 8, MaxRangeMS: 5000, MaxJobMS: 6000}
	source := Source{Path: "local.mp4", DurationMS: 10000}
	valid := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "separate", SubtitleMode: "omit", Ranges: []Range{{StartMS: 1000, EndMS: 2000, Label: " First "}}}
	if err := valid.Validate(c, source); err != nil {
		t.Fatal(err)
	}
	if valid.OutputMode != "" || valid.SubtitleMode != "" || valid.Ranges[0].Label != "First" {
		t.Fatal("defaults or labels not normalized", valid)
	}
	raw, _ := json.Marshal(valid)
	if strings.Contains(string(raw), "output_mode") || strings.Contains(string(raw), "subtitle_mode") {
		t.Fatal("legacy JSON defaults changed", string(raw))
	}
	for _, tc := range []struct {
		r       Range
		message string
	}{
		{Range{StartMS: -1, EndMS: 1000, Label: "Intro"}, "start_ms must be at least 0"},
		{Range{StartMS: 2000, EndMS: 1000, Label: "Intro"}, "set end_ms after start_ms"},
		{Range{StartMS: 9000, EndMS: 11000, Label: "Intro"}, "source duration"},
		{Range{StartMS: 0, EndMS: 6000, Label: "Intro"}, "shorten the range"},
	} {
		request := valid
		request.Ranges = []Range{{StartMS: 0, EndMS: 500}, tc.r}
		if err := request.Validate(c, source); err == nil || !strings.Contains(err.Error(), `range 2 ("Intro")`) || !strings.Contains(err.Error(), tc.message) {
			t.Fatal("missing contextual correction", err)
		}
	}
	index := 1
	for _, tc := range []struct {
		request ExportRequest
		source  Source
	}{
		{ExportRequest{Format: "mkv", CutMode: "accurate"}, source},
		{ExportRequest{Format: "mp3", NoAudio: true, CutMode: "accurate"}, source},
		{ExportRequest{Format: "mp4", NoAudio: true, AudioTrack: &index, CutMode: "accurate"}, source},
		{ExportRequest{Format: "mp4", AudioTrack: &index, CutMode: "accurate"}, Source{DurationMS: 10000}},
		{ExportRequest{Format: "mp4", SubtitleMode: "preserve", CutMode: "accurate"}, source},
	} {
		request := tc.request
		request.Quality = "best"
		request.Ranges = []Range{{EndMS: 1000}}
		if err := request.Validate(c, tc.source); err == nil {
			t.Fatal("incompatible options accepted", request)
		}
	}
}

func TestJoinedBudgetRetainsAllIntermediateProducers(t *testing.T) {
	c := Config{MaxOutputBytes: 16 << 30, MaxSourceBytes: 32 << 30, MaxFetchBytes: 32 << 30}
	r := ExportRequest{Format: "mp4", Quality: "best", CutMode: "copy", OutputMode: "joined", Ranges: []Range{{StartMS: 25000, EndMS: 27000}, {StartMS: 1000, EndMS: 3000}}}
	job := Job{Request: r, Items: jobItems(r)}
	reserve, err := remainingJobReserve(c, job, true)
	expected := joinedOutputBudget(c, r)
	var copyTransient int64
	for _, selected := range r.Ranges {
		budget := outputBudget(c, selected, r)
		copyTransient = max(copyTransient, budget)
		expected += budget + 2*inputBudget(c, selected, r)
	}
	expected += copyTransient
	if err != nil || reserve != expected {
		t.Fatal("joined producers omitted from reservation", reserve, expected, err)
	}
	job.Items[0].Status = "succeeded"
	job.Items[0].Artifact = &Artifact{ID: "published"}
	if reserve, err = remainingJobReserve(c, job, true); err != nil || reserve != 0 {
		t.Fatal("recovered completed join reserves media again", reserve, err)
	}
	c.MaxOutputBytes = math.MaxInt64
	r.Ranges = []Range{{EndMS: math.MaxInt64}}
	if _, err := joinedWorkBudget(c, r, false); err == nil {
		t.Fatal("joined intermediate overflow admitted")
	}
}

func TestHTTPTracksAreFreshOwnerProtectedAndRemoteExplicit(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.SourceTimeout = time.Minute
	c.MutationsPerMinute = 100
	c.MaxActiveJobs = 32
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	identity := httptest.NewRequest("GET", "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	path := filepath.Join(c.DataDir, "tracks.mkv")
	if _, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color=navy:size=160x90:rate=10", "-f", "lavfi", "-i", "sine=frequency=400", "-f", "lavfi", "-i", "sine=frequency=900", "-t", "1", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=rus", "-metadata:s:a:1", "title=Russian", path); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: newID("src"), Owner: owner, Path: path, Kind: "upload", DurationMS: 1000}
	if err := s.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	server := NewServer(c, s)
	request := func(id string, cookie *http.Cookie) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, databaseRequest("GET", "/api/v1/sources/"+id+"/tracks", "", cookie))
		return w
	}
	w := request(source.ID, cookie)
	var payload struct {
		Tracks []Track `json:"tracks"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil || len(payload.Tracks) != 3 || payload.Tracks[2].Index != 2 || payload.Tracks[2].Language != "rus" || payload.Tracks[2].Title != "Russian" {
		t.Fatal("track inspection metadata mismatch", w.Code, w.Body.String())
	}
	selectedAudio := 2
	export := ExportRequest{SourceID: source.ID, Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined", AudioTrack: &selectedAudio, Ranges: []Range{{StartMS: 500, EndMS: 900, Label: "Later"}, {EndMS: 400, Label: "Earlier"}}}
	accepted := recoveryJobResponse(t, server, cookie, export, "joined-audio")
	var joined Job
	if accepted.Code != 202 || json.Unmarshal(accepted.Body.Bytes(), &joined) != nil || len(joined.Items) != 1 {
		t.Fatal("joined audio HTTP job rejected", accepted.Code, accepted.Body.String())
	}
	persisted, err := s.Job(context.Background(), joined.ID, owner)
	if err != nil || persisted.Request.AudioTrack == nil || *persisted.Request.AudioTrack != 2 || persisted.Request.OutputMode != "joined" || persisted.Request.Ranges[0].Label != "Later" {
		t.Fatal("immutable advanced request not stored", persisted, err)
	}
	selectedAudio = 0
	invalid := recoveryJobResponse(t, server, cookie, export, "invalid-audio")
	if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), "audio_track_unavailable") {
		t.Fatal("video stream accepted as audio", invalid.Code, invalid.Body.String())
	}
	replayed := recoveryJobResponse(t, server, cookie, export, "joined-audio")
	if replayed.Code != 202 || !strings.Contains(replayed.Body.String(), joined.ID) {
		t.Fatal("new options broke retained owner/key replay", replayed.Code, replayed.Body.String())
	}
	identityBytes := make([]byte, 32)
	identityBytes[0] = 1
	foreign := &http.Cookie{Name: cookie.Name, Value: base64.RawURLEncoding.EncodeToString(identityBytes)}
	if w = request(source.ID, foreign); w.Code != 404 || strings.Contains(w.Body.String(), "Russian") {
		t.Fatal("tracks disclosed to other owner", w.Code, w.Body.String())
	}
	remote := Source{ID: newID("src"), Owner: owner, Kind: "platform", DurationMS: 1000, URL: "https://example.com/remote"}
	if err := s.AddSource(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	if w = request(remote.ID, cookie); w.Code != 422 || !strings.Contains(w.Body.String(), "tracks_unavailable") {
		t.Fatal("remote tracks unresolved semantics", w.Code, w.Body.String())
	}
	// A replacement local file is inspected freshly rather than persisting stale
	// language/title fields with the original source metadata.
	if err := os.WriteFile(path, []byte("no longer media"), 0600); err != nil {
		t.Fatal(err)
	}
	if w = request(source.ID, cookie); w.Code != 422 {
		t.Fatal("stale tracks returned after source changed", w.Code, w.Body.String())
	}
}

func TestJoinedWorkerShutdownRecoveryAndUserCancellation(t *testing.T) {
	for _, scenario := range []string{"shutdown", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			s := testStore(t)
			c := mediaConfig(t)
			c.MaxStorageBytes = 100 << 20
			c.JobTimeout = 20 * time.Second
			c.SourceTimeout = time.Second
			s.ConfigureStorage(c)
			source := workerSourceFixture(t, s, c)
			request := requestFor(source)
			request.OutputMode = "joined"
			request.Ranges = []Range{{StartMS: 2000, EndMS: 3000, Label: "Later"}, {StartMS: 0, EndMS: 1000, Label: "Earlier"}}
			if _, err := s.CreateJob(context.Background(), source.Owner, request, "join-recovery"); err != nil {
				t.Fatal(err)
			}
			job, token, err := s.Claim(context.Background())
			if err != nil || len(job.Items) != 1 || job.Items[0].EndMS != 2000 {
				t.Fatal("joined job item shape", job, err)
			}
			marker := filepath.Join(c.DataDir, "started")
			blocked := c
			blocked.FFmpeg = filepath.Join(c.DataDir, "blocked")
			quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
			if err := os.WriteFile(blocked.FFmpeg, []byte("#!/bin/sh\n: > "+quoted+"\nsleep 60\n"), 0700); err != nil {
				t.Fatal(err)
			}
			parent, stop := context.WithCancel(context.Background())
			defer stop()
			done := make(chan struct{})
			go func() { processJob(parent, blocked, s, job, token); close(done) }()
			waitForWorkerFile(t, marker)
			if scenario == "cancel" {
				if err := s.Cancel(context.Background(), job.ID, source.Owner); err != nil {
					t.Fatal(err)
				}
			} else {
				stop()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("joined media process not stopped")
			}
			interrupted, err := s.Job(context.Background(), job.ID, source.Owner)
			if err != nil {
				t.Fatal(err)
			}
			_, reserved := ledgerBytes(t, s)
			if reserved != 0 {
				t.Fatal("joined cancellation/shutdown retained storage", reserved)
			}
			if scenario == "cancel" {
				if interrupted.Status != "cancelled" || interrupted.Items[0].ErrorCode != "cancelled" || interrupted.Items[0].Artifact != nil {
					t.Fatal("joined cancellation semantics", interrupted)
				}
				return
			}
			if interrupted.Status != "running" || interrupted.Items[0].Status != "running" {
				t.Fatal("shutdown cannot resume", interrupted)
			}
			if _, err := s.DB.Exec(context.Background(), `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			resumed, newToken, err := s.Claim(context.Background())
			if err != nil || newToken == token {
				t.Fatal("joined recovery claim", err)
			}
			if err := s.SaveJob(context.Background(), interrupted, token); !errors.Is(err, ErrNotFound) {
				t.Fatal("old lease can write joined state", err)
			}
			processJob(context.Background(), c, s, resumed, newToken)
			finished, err := s.Job(context.Background(), job.ID, source.Owner)
			if err != nil || finished.Status != "succeeded" || len(finished.Items) != 1 || finished.Items[0].Artifact == nil {
				t.Fatal("joined recovery export", finished, err)
			}
			artifact := finished.Items[0].Artifact
			if artifact.Segments == nil || len(*artifact.Segments) != 2 || (*artifact.Segments)[0].Label != "Later" || (*artifact.Segments)[0].SourceStartMS != 2000 || (*artifact.Segments)[1].SourceStartMS != 0 || artifact.TrackManifest == nil {
				t.Fatal("joined actual order/track manifest missing", artifact)
			}
			output, _, err := s.ArtifactPath(context.Background(), artifact.ID, source.Owner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-i", output, "-f", "null", "-"); err != nil {
				t.Fatal("joined result does not decode", err)
			}
			if _, reserved = ledgerBytes(t, s); reserved != 0 {
				t.Fatal("joined recovery intermediate storage retained", reserved)
			}
		})
	}
}

func TestSeparateBatchPartialFailureKeepsCompletedDownload(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes = 100 << 20
	c.JobTimeout = 20 * time.Second
	c.SourceTimeout = time.Second
	s.ConfigureStorage(c)
	source := workerSourceFixture(t, s, c)
	request := requestFor(source)
	request.Ranges = append(request.Ranges, Range{StartMS: 0, EndMS: 1000, Label: "Second"})
	if _, err := s.CreateJob(context.Background(), source.Owner, request, ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	processJobWithPublisher(context.Background(), c, s, job, token, func(ctx context.Context, j Job, path, token string, a Artifact) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("injected publication failure")
		}
		return s.PublishArtifact(ctx, j, path, token, a)
	})
	finished, err := s.Job(context.Background(), job.ID, source.Owner)
	if err != nil || finished.Status != "failed" || finished.Items[0].Status != "succeeded" || finished.Items[0].Artifact == nil || finished.Items[1].Status != "failed" || finished.Items[1].Artifact != nil {
		t.Fatal("partial batch lost completed artifact", finished, err)
	}
	if path, _, err := s.ArtifactPath(context.Background(), finished.Items[0].Artifact.ID, source.Owner); err != nil {
		t.Fatal(err)
	} else if _, err := os.Stat(path); err != nil {
		t.Fatal("partial batch download missing", err)
	}
}

func TestJoinedLostPublicationAcknowledgementResumesWithoutRendering(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes = 100 << 20
	c.JobTimeout = 20 * time.Second
	c.SourceTimeout = time.Second
	s.ConfigureStorage(c)
	source := workerSourceFixture(t, s, c)
	request := requestFor(source)
	request.OutputMode = "joined"
	request.Ranges = []Range{{StartMS: 2000, EndMS: 3000, Label: "Later"}, {EndMS: 1000, Label: "Earlier"}}
	if _, err := s.CreateJob(context.Background(), source.Owner, request, ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	processJobWithPublisher(context.Background(), c, s, job, token, func(ctx context.Context, j Job, path, token string, a Artifact) error {
		if err := s.PublishArtifact(ctx, j, path, token, a); err != nil {
			return err
		}
		return &ArtifactPublicationError{CommitUncertain: true, cause: errors.New("lost acknowledgement")}
	})
	saved, err := s.Job(context.Background(), job.ID, source.Owner)
	if err != nil || saved.Status != "running" || saved.Items[0].Status != "succeeded" || saved.Items[0].Artifact == nil || saved.Items[0].Artifact.Segments == nil {
		t.Fatal("joined committed result snapshot lost", saved, err)
	}
	id := saved.Items[0].Artifact.ID
	if _, err := s.DB.Exec(context.Background(), `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	resumed, newToken, err := s.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.FFmpeg = "unavailable-on-recovery"
	c.FFprobe = "unavailable-on-recovery"
	processJob(context.Background(), c, s, resumed, newToken)
	finished, err := s.Job(context.Background(), job.ID, source.Owner)
	if err != nil || finished.Status != "succeeded" || finished.Items[0].Artifact == nil || finished.Items[0].Artifact.ID != id || len(*finished.Items[0].Artifact.Segments) != 2 {
		t.Fatal("joined result rendered twice or changed on recovery", finished, err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 0 {
		t.Fatal("completed recovery retained storage", reserved)
	}
}

func TestTrackInspectionPinsSourceSharesPreviewBudgetAndCancels(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.SourceTimeout = time.Minute
	c.SourceTTL = time.Minute
	c.MutationsPerMinute = 100
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	identity := httptest.NewRequest("GET", "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	source := storedSource(t, s, owner)
	if _, err := s.DB.Exec(context.Background(), "UPDATE sources SET created_at=now()-interval '2 hours' WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(c.DataDir, "inspection-started")
	c.FFprobe = filepath.Join(c.DataDir, "blocked-ffprobe")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	if err := os.WriteFile(c.FFprobe, []byte("#!/bin/sh\n: > "+quoted+"\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server := NewServer(c, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	response := httptest.NewRecorder()
	go func() {
		server.Handler().ServeHTTP(response, databaseRequest("GET", "/api/v1/sources/"+source.ID+"/tracks", "", cookie).WithContext(ctx))
		close(done)
	}()
	waitForWorkerFile(t, marker)
	if _, err := s.DeleteSource(context.Background(), source.ID, owner); !errors.Is(err, ErrSourceInUse) {
		t.Fatal("active inspection does not pin explicit deletion", err)
	}
	if _, err := s.Cleanup(context.Background(), c.ArtifactTTL, c.SourceTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Source(context.Background(), source.ID, owner); err != nil {
		t.Fatal("active inspection does not pin source TTL", err)
	}
	overloaded := httptest.NewRecorder()
	server.Handler().ServeHTTP(overloaded, databaseRequest("GET", "/api/v1/sources/"+source.ID+"/tracks", "", cookie))
	if overloaded.Code != 429 || !strings.Contains(overloaded.Body.String(), "server_busy") {
		t.Fatal("track probes bypass preview processing slots", overloaded.Code, overloaded.Body.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled inspection process survived")
	}
	if response.Body.Len() != 0 {
		t.Fatal("cancelled caller received error payload", response.Body.String())
	}
	var active, pins int
	if err := s.DB.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM storage_reservations WHERE kind='preview'),retention_preview_pins FROM sources WHERE id=$1", source.ID).Scan(&active, &pins); err != nil || active != 0 || pins != 0 {
		t.Fatal("inspection cancellation retained admission/source pins", active, pins, err)
	}
	// An interrupted API process can leave no workspace. Existing preview
	// maintenance must still remove its expired zero-byte inspection pin.
	if err := s.reserveSourceInspection(context.Background(), source, "inspection-expired", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(context.Background(), "UPDATE storage_reservations SET expires_at=now()-interval '1 second' WHERE id='inspection-expired'"); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupPreviews(context.Background(), c); err != nil {
		t.Fatal("expired inspection pin requires a nonexistent workspace", err)
	}
	if err := s.DB.QueryRow(context.Background(), "SELECT retention_preview_pins FROM sources WHERE id=$1", source.ID).Scan(&pins); err != nil || pins != 0 {
		t.Fatal("expired inspection still pins source", pins, err)
	}
}
