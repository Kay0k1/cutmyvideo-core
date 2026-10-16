package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func httpCollectionRequest(t *testing.T, server *Server, cookie *http.Cookie, route string, request any, key string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://example.com"+route, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	return w
}

func remoteCollectionFixture(t *testing.T, count int) (*Store, *Server, *http.Cookie, string, CollectionRequest) {
	t.Helper()
	store, _, server, cookie, owner := httpPreflightFixture(t)
	source := Source{ID: newID("src"), Owner: owner, Title: "Collection interview", Kind: "platform", Provider: "vimeo", URL: "https://127.0.0.1:1/private?credential=never-expose", DurationMS: 10000, Width: 1920, Height: 1080}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	server.Config.FFmpeg, server.Config.FFprobe, server.Config.YTDLP = "/must-not-launch", "/must-not-launch", "/must-not-launch"
	request := CollectionRequest{SourceID: source.ID, Plan: CollectionPlan{Version: 1}}
	for i := range count {
		request.Plan.Clips = append(request.Plan.Clips, CollectionClip{ID: fmt.Sprintf("clip-%d", i+1), Label: fmt.Sprintf("Moment %d", i+1), Ranges: []Range{{StartMS: int64(i * 1000), EndMS: int64(i*1000 + 500)}}})
	}
	return store, server, cookie, owner, request
}

func collectionSubmissionReport(t *testing.T, w *httptest.ResponseRecorder, status int) CollectionSubmission {
	t.Helper()
	if w.Code != status {
		t.Fatalf("collection returned %d; want %d: %s", w.Code, status, w.Body.String())
	}
	var result CollectionSubmission
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Version != 1 || len(result.Clips) == 0 {
		t.Fatalf("invalid collection submission: %s (%v)", w.Body.String(), err)
	}
	for _, clip := range result.Clips {
		if (clip.Job == nil) == (clip.Error == nil) {
			t.Fatalf("clip must have exactly one job or error: %+v", clip)
		}
	}
	return result
}

func TestHTTPCollectionsRemoteValidationAndGuards(t *testing.T) {
	store, server, cookie, _, request := remoteCollectionFixture(t, 2)
	request.Plan.Clips[0].Ranges = append(request.Plan.Clips[0].Ranges, Range{StartMS: 3000, EndMS: 4000})
	w := httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, "ignored")
	var result CollectionValidation
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || !result.Valid || len(result.Clips) != 2 || result.RequestedMS != 2000 {
		t.Fatalf("remote collection report: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	for _, clip := range result.Clips {
		if clip.Preflight.Request.OutputMode != "joined" || clip.Preflight.OutputCount != 1 || preflightCheckStatus(clip.Preflight, "source_media") != "deferred" || preflightCheckStatus(clip.Preflight, "tool_capabilities") != "deferred" {
			t.Fatal("remote preflight claimed media inspection or lost clip assembly", clip)
		}
	}
	if strings.Contains(w.Body.String(), "127.0.0.1") || strings.Contains(w.Body.String(), "credential") || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("collection report leaked private media information")
	}
	assertHTTPPreflightNoWork(t, store, 0)
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, nil, "/api/v1/collections/validate", request, ""), 401, "session_required")
	other := request
	other.SourceID = "not-owned"
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", other, ""), 404, "not_found")
	other = request
	other.Plan.Input = "/server/private-file"
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", other, ""), 400, "invalid_collection")
	other = request
	other.ClipIDs = []string{"missing"}
	w = httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", other, "")
	assertHTTPPreflightError(t, w, 400, "invalid_collection")
	if !strings.Contains(w.Body.String(), `"clip_id":"missing"`) || !strings.Contains(w.Body.String(), `"field":"clip_ids[0]"`) {
		t.Fatal("selection error lost field attribution", w.Body.String())
	}
	other.ClipIDs = []string{"clip-1", "clip-1"}
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", other, ""), 400, "invalid_collection")
	server.Config.MaxRanges = 1
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, ""), 400, "invalid_collection")
	server.Config.MaxRanges = 12
	server.Config.MaxJobMS = 1000
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, ""), 400, "invalid_collection")
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/collections/validate", bytes.NewReader(data))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://foreign.example")
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	assertHTTPPreflightError(t, w, 403, "origin_rejected")
	r = httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/collections/validate", strings.NewReader(`{"source_id":"`+request.SourceID+`","plan":{"version":1,"clips":[],"unknown":true}}`))
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	assertHTTPPreflightError(t, w, 400, "invalid_request")
	r = httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/collections/validate", strings.NewReader(`{"source_id":"`+strings.Repeat("x", 1<<20)+`"}`))
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	assertHTTPPreflightError(t, w, 400, "invalid_request")
	assertHTTPPreflightNoWork(t, store, 0)
}

func TestHTTPCollectionsSubmitRetryAndSelectedEdit(t *testing.T) {
	store, server, cookie, owner, request := remoteCollectionFixture(t, 5)
	const route = "/api/v1/collections"
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, route, request, ""), 400, "invalid_collection")
	first := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "interview-1"), 202)
	if first.Complete || len(first.Clips) != 5 {
		t.Fatal("queue limits were bypassed", first)
	}
	for i, clip := range first.Clips {
		if i < 3 {
			if clip.Job == nil || clip.Job.Status != "queued" || len(clip.Job.Items) != 1 {
				t.Fatal("accepted clip was not an ordinary joined job", clip)
			}
			if err := store.Cancel(context.Background(), clip.Job.ID, owner); err != nil {
				t.Fatal(err)
			}
		} else if clip.Job != nil || clip.Error.Code != "job_limit" || !clip.Retryable {
			t.Fatal("partial submission did not expose retryable pending clips", clip)
		}
	}
	retried := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "interview-1"), 202)
	if !retried.Complete {
		t.Fatal("retry did not admit remaining clips", retried)
	}
	for i := range 3 {
		if retried.Clips[i].Job.ID != first.Clips[i].Job.ID || retried.Clips[i].Job.Status != "cancelled" {
			t.Fatal("retry recreated retained work", retried.Clips[i])
		}
	}
	for i := 3; i < 5; i++ {
		if err := store.Cancel(context.Background(), retried.Clips[i].Job.ID, owner); err != nil {
			t.Fatal(err)
		}
	}
	request.ClipIDs = []string{"clip-2"}
	request.Plan.Clips[1].Ranges[0].EndMS += 100
	edited := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "interview-1"), 202)
	if !edited.Complete || len(edited.Clips) != 1 || edited.Clips[0].Job.ID == retried.Clips[1].Job.ID || edited.Clips[0].Request.Ranges[0].EndMS != 1600 {
		t.Fatal("selected edit did not create exactly the changed clip", edited)
	}
	var jobs int
	if err := store.DB.QueryRow(context.Background(), "SELECT count(*) FROM jobs").Scan(&jobs); err != nil || jobs != 6 {
		t.Fatalf("selected retry created unrelated jobs: count=%d err=%v", jobs, err)
	}
	// Changes to unselected clips do not alter the selected clip's identity.
	request.Plan.Clips[0].Ranges[0].EndMS += 100
	same := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "interview-1"), 202)
	if same.Clips[0].Job.ID != edited.Clips[0].Job.ID {
		t.Fatal("unselected edit invalidated accepted clip")
	}
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=true WHERE id=$1", request.SourceID); err != nil {
		t.Fatal(err)
	}
	// Recovery precedes media removal, inspection and new-export allowance.
	server.rate["export:"+owner] = &rateEntry{count: 10, reset: server.rateCleanup}
	recovered := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "interview-1"), 202)
	if recovered.Clips[0].Job.ID != edited.Clips[0].Job.ID {
		t.Fatal("retained result became inaccessible after source removal")
	}
	request.ClipIDs = []string{"clip-1"}
	blocked := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "interview-1"), 410)
	if blocked.Clips[0].Error.Code != "source_removed" || blocked.Clips[0].Retryable {
		t.Fatal("new edit incorrectly admitted removed media", blocked)
	}
}

func TestHTTPCollectionsLocalPreflightAndDraft(t *testing.T) {
	_ = mediaConfig(t)
	store, config, server, cookie, owner := httpPreflightFixture(t)
	config.JobTimeout = 30 * time.Second
	config.MaxStorageBytes = 128 << 20
	store.ConfigureStorage(config)
	source := addHTTPPreflightVideo(t, store, config, owner)
	request := CollectionRequest{SourceID: source.ID, Rendition: "draft", Plan: CollectionPlan{Version: 1,
		Clips: []CollectionClip{{ID: "highlight", Label: "Joined highlight", Ranges: []Range{{StartMS: 2000, EndMS: 3000}, {StartMS: 0, EndMS: 1000}}, Options: CollectionOptions{Crop: &Crop{X: .25, Y: 0, Width: .5, Height: 1}}}},
	}}
	if _, err := store.DB.Exec(context.Background(), `INSERT INTO source_media_readers(id,source_id,expires_at) SELECT 'collection-reader-'||i,$1,now()+interval '1 minute' FROM generate_series(1,128) i`, source.ID); err != nil {
		t.Fatal(err)
	}
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, ""), 503, "server_busy")
	blocked := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections", request, "local-draft"), 503)
	if blocked.Clips[0].Error.Code != "server_busy" || !blocked.Clips[0].Retryable {
		t.Fatal("inspection admission was not reported as retryable", blocked)
	}
	if _, err := store.DB.Exec(context.Background(), "DELETE FROM source_media_readers WHERE id LIKE 'collection-reader-%'"); err != nil {
		t.Fatal(err)
	}
	assertHTTPPreflightNoWork(t, store, 0)
	w := httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, "")
	var validation CollectionValidation
	if err := json.Unmarshal(w.Body.Bytes(), &validation); err != nil || w.Code != 200 || !validation.Valid || validation.Clips[0].Preflight.Video.Width != 80 || validation.Clips[0].Preflight.Video.Height != 96 || validation.Clips[0].Preflight.Request.Quality != "720p" || validation.Clips[0].Preflight.Request.Rendition != "draft" {
		t.Fatalf("local draft preflight lost crop or rendition: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	assertHTTPPreflightNoWork(t, store, 0)
	for _, directory := range []string{"artifacts", "work"} {
		if err := os.MkdirAll(filepath.Join(config.DataDir, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	submission := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections", request, "local-draft"), 202)
	job, token, err := store.Claim(context.Background())
	if err != nil || job.ID != submission.Clips[0].Job.ID || job.Request.Rendition != "draft" {
		t.Fatal("queue did not persist the draft rendition", job, err)
	}
	processJob(context.Background(), config, store, job, token)
	finished, err := store.Job(context.Background(), job.ID, owner)
	if err != nil || finished.Status != "succeeded" || len(finished.Items) != 1 || finished.Items[0].Artifact == nil || finished.Items[0].Artifact.Segments == nil {
		t.Fatalf("draft joined export failed: %+v %v", finished, err)
	}
	artifact := finished.Items[0].Artifact
	segments := *artifact.Segments
	if len(segments) != 2 || segments[0].SourceStartMS != 2000 || segments[1].SourceStartMS != 0 || segments[1].OutputStartMS != 1000 {
		t.Fatal("draft lost submitted order or source mapping", segments)
	}
	path, _, err := store.ArtifactPath(context.Background(), artifact.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	p, duration, err := probe(context.Background(), config, path, false)
	if err != nil || p.selectedVideo().Width != 80 || p.selectedVideo().Height != 96 || duration < 1950 || duration > 2100 {
		t.Fatalf("rendered draft geometry/duration mismatch: %+v %d %v", p, duration, err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	request.Rendition = "final"
	final := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections", request, "local-draft"), 202)
	if final.Clips[0].Job.ID == job.ID || final.Clips[0].Request.Rendition != "" {
		t.Fatal("final export reused a draft job", final)
	}
	request.Plan.Clips[0].Options.AudioTrack = new(int)
	*request.Plan.Clips[0].Options.AudioTrack = 99
	w = httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, "")
	assertHTTPPreflightError(t, w, 422, "audio_track_unavailable")
	if !strings.Contains(w.Body.String(), `"clip_id":"highlight"`) || !strings.Contains(w.Body.String(), `"field":"options.audio_track"`) {
		t.Fatal("local blocker lost clip/field attribution", w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(after) != digest {
		t.Fatal("failed later validation changed an existing output", err)
	}
	// Download remains an existing owner-protected artifact endpoint.
	for _, owned := range []bool{true, false} {
		r := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/artifacts/"+artifact.ID+"/download", nil)
		if owned {
			r.AddCookie(cookie)
		} else {
			r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))})
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if owned && (w.Code != 200 || !bytes.Equal(w.Body.Bytes(), original)) || !owned && w.Code != 404 {
			t.Fatal("collection artifact download ownership changed", owned, w.Code)
		}
	}
}

func TestHTTPCollectionsBatchPreflightRejectsLateClipBeforeAdmission(t *testing.T) {
	_ = mediaConfig(t)
	store, config, server, cookie, owner := httpPreflightFixture(t)
	source := addHTTPPreflightVideo(t, store, config, owner)
	config.MaxRanges = 128
	commands := countMediaCommands(t, &config)
	server.Config = config
	request := CollectionRequest{SourceID: source.ID, Plan: CollectionPlan{Version: 1}}
	for i := range 15 {
		clip := CollectionClip{ID: fmt.Sprintf("clip-%02d", i+1), Ranges: []Range{{StartMS: int64(i * 20), EndMS: int64(i*20 + 500)}}}
		if i%3 == 1 {
			clip.Options.Crop = &Crop{X: .25, Y: 0, Width: .5, Height: 1}
		}
		if i%3 == 2 {
			value := true
			clip.Options.NoAudio = &value
		}
		request.Plan.Clips = append(request.Plan.Clips, clip)
	}
	for attempt := range 2 {
		w := httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, "")
		var validation CollectionValidation
		if err := json.Unmarshal(w.Body.Bytes(), &validation); err != nil || w.Code != http.StatusOK || !validation.Valid || len(validation.Clips) != 15 {
			t.Fatalf("batch validation failed: status=%d err=%v body=%s", w.Code, err, w.Body.String())
		}
		if got := len(commands()); got != (attempt+1)*5 {
			t.Fatalf("HTTP request did not use one fresh probe/four listings: got %d commands after %d requests", got, attempt+1)
		}
		assertHTTPPreflightNoWork(t, store, 0)
	}
	// A valid range but missing explicit audio track is discovered during media
	// preflight, after all earlier clips have been checked. Nothing is admitted.
	index := 99
	request.Plan.Clips[14].Options.NoAudio = nil
	request.Plan.Clips[14].Options.AudioTrack = &index
	failed := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections", request, "late-invalid-clip"), http.StatusUnprocessableEntity)
	for _, clip := range failed.Clips {
		if clip.Job != nil || clip.Error.Code != "audio_track_unavailable" || clip.Error.ClipID != "clip-15" || clip.Error.Field != "options.audio_track" {
			t.Fatal("late preflight blocker lost attribution or admitted an earlier clip", clip)
		}
	}
	if got := len(commands()); got != 15 {
		t.Fatalf("failed batch ran unexpected extra tool processes: %d", got)
	}
	assertHTTPPreflightNoWork(t, store, 0)
}

func TestHTTPCollectionsPartialFailureRecovery(t *testing.T) {
	store, server, cookie, owner, request := remoteCollectionFixture(t, 2)
	request.ClipIDs = []string{"clip-1"}
	first := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections", request, "partial"), 202)
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=true WHERE id=$1", request.SourceID); err != nil {
		t.Fatal(err)
	}
	request.ClipIDs = nil
	partial := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, "/api/v1/collections", request, "partial"), 202)
	if partial.Complete || partial.Clips[0].Job.ID != first.Clips[0].Job.ID || partial.Clips[1].Error.Code != "source_removed" {
		t.Fatal("recovery hid a retained job behind pending source failure", partial)
	}
	jobs, err := store.Jobs(context.Background(), owner)
	if err != nil || len(jobs) != 1 {
		t.Fatal("partial failure created extra work", jobs, err)
	}
}

func TestHTTPCollectionsRetainedRecoveryAfterSourceChanges(t *testing.T) {
	_ = mediaConfig(t)
	store, config, server, cookie, owner := httpPreflightFixture(t)
	path := filepath.Join(config.DataDir, "audio-interview.mp4")
	mcRun(t, context.Background(), config, "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25", "-f", "lavfi", "-i", "sine=frequency=800:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", path)
	source := Source{ID: newID("src"), Owner: owner, Title: "Retained interview", Kind: "upload", Path: path, DurationMS: 4000, Width: 160, Height: 96}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	track := 1
	request := CollectionRequest{SourceID: source.ID, ClipIDs: []string{"clip-1", "clip-2"}, Plan: CollectionPlan{Version: 1, Options: CollectionOptions{AudioTrack: &track}}}
	for i := range 3 {
		request.Plan.Clips = append(request.Plan.Clips, CollectionClip{ID: fmt.Sprintf("clip-%d", i+1), Ranges: []Range{{StartMS: 1000, EndMS: 2000}, {StartMS: 0, EndMS: 500}}})
	}
	const route = "/api/v1/collections"
	accepted := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "retained"), 202)
	if !accepted.Complete || len(accepted.Clips) != 2 {
		t.Fatal("initial indexed-audio clips were not admitted", accepted)
	}
	server.Config.FFprobe, server.Config.FFmpeg = "/must-not-launch", "/must-not-launch"
	server.Config.MaxRanges, server.Config.MaxRangeMS, server.Config.MaxJobMS = 1, 1, 1
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET duration_ms=100 WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	recovered := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "retained"), 202)
	if !recovered.Complete || recovered.Clips[0].Job.ID != accepted.Clips[0].Job.ID || recovered.Clips[1].Job.ID != accepted.Clips[1].Job.ID {
		t.Fatal("source duration or tightened operator limits blocked retained recovery", recovered)
	}
	request.ClipIDs = nil
	partial := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "retained"), 202)
	if partial.Complete || partial.Clips[0].Job.ID != accepted.Clips[0].Job.ID || partial.Clips[1].Job.ID != accepted.Clips[1].Job.ID || partial.Clips[2].Error.Code != "invalid_collection" || partial.Clips[2].Retryable {
		t.Fatal("relaxed identity validation admitted new invalid work or hid retained jobs", partial)
	}
	// Actual successful source cleanup clears path as well as media_removed.
	// The explicit audio stream remains part of the original clip identity.
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET path='',media_removed=true WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	request.ClipIDs = []string{"clip-1", "clip-2"}
	recovered = collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "retained"), 202)
	if !recovered.Complete || recovered.Clips[0].Job.ID != accepted.Clips[0].Job.ID || recovered.Clips[1].Job.ID != accepted.Clips[1].Job.ID {
		t.Fatal("cleanup of indexed-track source blocked retained recovery", recovered)
	}
	request.ClipIDs = []string{"clip-1", "clip-1"}
	assertHTTPPreflightError(t, httpCollectionRequest(t, server, cookie, route, request, "retained"), 400, "invalid_collection")
	request.ClipIDs = []string{"clip-3"}
	blocked := collectionSubmissionReport(t, httpCollectionRequest(t, server, cookie, route, request, "retained"), 410)
	if blocked.Clips[0].Error.Code != "source_removed" || blocked.Clips[0].Retryable {
		t.Fatal("relaxed identity validation admitted a new clip from removed media", blocked)
	}
	var jobs, readers, inspections int
	if err := store.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM jobs),(SELECT count(*) FROM source_media_readers),(SELECT count(*) FROM storage_reservations WHERE kind='preview')`).Scan(&jobs, &readers, &inspections); err != nil || jobs != 2 || readers != 0 || inspections != 0 {
		t.Fatalf("recovery created new jobs or leaked media admissions: jobs=%d readers=%d inspections=%d err=%v", jobs, readers, inspections, err)
	}
}

func TestHTTPCollectionsPublicContract(t *testing.T) {
	python := os.Getenv("CUTMY_CONTRACT_PYTHON")
	if python == "" {
		python = "python3"
	}
	if output, err := exec.Command(python, "-c", "import yaml,jsonschema").CombinedOutput(); err != nil {
		if os.Getenv("CUTMY_REQUIRE_CONTRACT") == "1" {
			t.Fatalf("contract dependencies required: %v %s", err, output)
		}
		t.Skip("OpenAPI runtime validation needs scripts/requirements-contract.txt")
	}
	store, server, cookie, _, request := remoteCollectionFixture(t, 5)
	var samples []map[string]any
	record := func(name, route string, request any, w *httptest.ResponseRecorder) {
		t.Helper()
		var body any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		headers := map[string]string{}
		for key := range w.Header() {
			headers[key] = w.Header().Get(key)
		}
		samples = append(samples, map[string]any{"name": name, "method": "POST", "route": route, "request": request, "status": w.Code, "headers": headers, "body": body})
	}
	validate := "/api/v1/collections/validate"
	submit := "/api/v1/collections"
	w := httpCollectionRequest(t, server, cookie, validate, request, "")
	record("collection-remote-validation", validate, request, w)
	request.Rendition = "draft"
	w = httpCollectionRequest(t, server, cookie, validate, request, "")
	record("collection-remote-draft-validation", validate, request, w)
	request.Rendition = "final"
	w = httpCollectionRequest(t, server, cookie, submit, request, "contract-collection")
	collectionSubmissionReport(t, w, 202)
	record("collection-partial-admission", submit, request, w)
	w = httpCollectionRequest(t, server, cookie, submit, request, "contract-collection")
	collectionSubmissionReport(t, w, 202)
	record("collection-partial-recovery", submit, request, w)
	request.ClipIDs = []string{"clip-1"}
	w = httpCollectionRequest(t, server, cookie, submit, request, "contract-collection")
	record("collection-selected-recovery", submit, request, w)
	w = httpCollectionRequest(t, server, nil, validate, request, "")
	record("collection-session-required", validate, request, w)
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=true WHERE id=$1", request.SourceID); err != nil {
		t.Fatal(err)
	}
	request.ClipIDs = []string{"clip-5"}
	w = httpCollectionRequest(t, server, cookie, submit, request, "contract-collection")
	collectionSubmissionReport(t, w, 410)
	record("collection-source-removed", submit, request, w)
	request.ClipIDs = []string{"missing"}
	w = httpCollectionRequest(t, server, cookie, validate, request, "")
	record("collection-invalid-selection", validate, request, w)
	data, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "collection-samples.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(python, filepath.Join("..", "..", "scripts", "check-contract.py"), "--samples", path).CombinedOutput(); err != nil {
		t.Fatalf("collection HTTP/OpenAPI contract: %v\n%s", err, output)
	}
}
