package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

func renderServiceFixture(t *testing.T) (*Store, Config, *Server, *http.Cookie, Source, Source) {
	t.Helper()
	_ = mediaConfig(t)
	store, c, server, cookie, owner := httpPreflightFixture(t)
	c.JobTimeout, c.MaxStorageBytes = 30*time.Second, 128<<20
	c.WorkerConcurrency, c.WorkerPreparationConcurrency = 2, 1
	server.Config = c
	store.ConfigureStorage(c)
	primary := addHTTPPreflightVideo(t, store, c, owner)
	audio := Source{ID: newID("src"), Owner: owner, Title: "External audio", Kind: "upload", Path: filepath.Join(c.DataDir, "external.wav"), DurationMS: 4000}
	mcRun(t, context.Background(), c, "-f", "lavfi", "-i", "sine=frequency=997:sample_rate=48000:duration=4", "-c:a", "pcm_s16le", audio.Path)
	if err := store.AddSource(context.Background(), audio); err != nil {
		t.Fatal(err)
	}
	return store, c, server, cookie, primary, audio
}

func transformedServiceRequest(source, audio Source) ExportRequest {
	i, p, l := -18.0, -2.0, 11.0
	r := requestFor(source)
	r.Quality, r.OutputMode = "best", "joined"
	r.Ranges = []Range{{StartMS: 2000, EndMS: 3000}, {StartMS: 0, EndMS: 1000}}
	r.Canvas = &edit.Canvas{Width: 192, Height: 192, Fit: "contain", Background: "#123456"}
	r.AudioNormalize = &edit.AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &p, LoudnessRangeLU: &l}
	r.AudioReplace = &edit.AudioReplace{SourceID: audio.ID, OffsetMS: 250}
	return r
}

func renderJobRequest(t *testing.T, server *Server, cookie *http.Cookie, request ExportRequest, key string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://example.com/api/v1/jobs", strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	return w
}

func TestRenderServicePreflightReferencesAndCleanup(t *testing.T) {
	store, c, server, cookie, source, audio := renderServiceFixture(t)
	r := transformedServiceRequest(source, audio)
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	invalidOffset := strings.Replace(string(encoded), `"offset_ms":250`, `"offset_ms":null`, 1)
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, "/api/v1/jobs/preflight", invalidOffset), 400, "invalid_request")
	report := httpPreflightReport(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, r))
	if report.Video == nil || report.Video.Width != 192 || report.Video.Height != 192 || report.Request.AudioReplace.SourceID != audio.ID {
		t.Fatalf("transformed preflight mismatch: %+v", report)
	}
	assertHTTPPreflightNoWork(t, store, 0)
	for _, change := range []func(*ExportRequest){
		func(r *ExportRequest) { r.AudioReplace = &edit.AudioReplace{Input: audio.Path} },
		func(r *ExportRequest) { r.AudioReplace = &edit.AudioReplace{SourceID: source.ID} },
	} {
		bad := transformedServiceRequest(source, audio)
		change(&bad)
		assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, bad), 400, "invalid_export")
		assertHTTPPreflightError(t, renderJobRequest(t, server, cookie, bad, ""), 400, "invalid_export")
	}
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET owner='someone-else' WHERE id=$1", audio.ID); err != nil {
		t.Fatal(err)
	}
	for _, response := range []*httptest.ResponseRecorder{
		httpPreflightRequest(t, server.Handler(), context.Background(), cookie, r),
		renderJobRequest(t, server, cookie, r, ""),
	} {
		assertHTTPPreflightError(t, response, 404, "not_found")
		if strings.Contains(response.Body.String(), c.DataDir) {
			t.Fatal("private replacement path leaked")
		}
	}
	assertHTTPPreflightNoWork(t, store, 0)
}

func TestRenderServiceProjectGrantDoesNotAuthorizeSecondaryMedia(t *testing.T) {
	source := Source{ID: "primary", Owner: "owner", Path: "primary.mp4", Kind: "upload"}
	r := ExportRequest{AudioReplace: &edit.AudioReplace{SourceID: "secondary"}}
	ctx := context.WithValue(context.Background(), projectScopeKey{}, projectScope{Actor: "recipient"})
	if err := checkHTTPRenderReference(ctx, source, r); err == nil {
		t.Fatal("primary project grant authorized another recording")
	}
	ctx = context.WithValue(context.Background(), projectScopeKey{}, projectScope{Actor: "owner"})
	if err := checkHTTPRenderReference(ctx, source, r); err != nil {
		t.Fatal("owner cannot bind own secondary recording", err)
	}
}

func TestRenderServiceWorkerTransformsAndRetainedRecovery(t *testing.T) {
	store, c, server, cookie, source, audio := renderServiceFixture(t)
	r := transformedServiceRequest(source, audio)
	w := renderJobRequest(t, server, cookie, r, "transformed")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	job, token, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var pinned bool
	processJobWithPublisher(withWorkerMediaBudget(context.Background(), c), c, store, job, token, func(ctx context.Context, j Job, path, token string, artifact Artifact) error {
		var readers int
		if err := store.DB.QueryRow(ctx, "SELECT count(*) FROM source_media_readers WHERE source_id=$1", audio.ID).Scan(&readers); err != nil {
			return err
		}
		pinned = readers == 1
		if err := localmedia.CheckAudioReplacement(ctx); err != nil {
			return err
		}
		return store.PublishArtifact(ctx, j, path, token, artifact)
	})
	finished, err := store.Job(context.Background(), job.ID, source.Owner)
	if err != nil || finished.Status != "succeeded" || !pinned || finished.Items[0].Artifact == nil {
		t.Fatalf("transformed worker failed: %+v pinned=%v err=%v", finished, pinned, err)
	}
	a := finished.Items[0].Artifact
	if a.Segments == nil || len(*a.Segments) != 2 || (*a.Segments)[0].SourceStartMS != 2000 || (*a.Segments)[1].OutputStartMS != 1000 {
		t.Fatal("joined source mapping changed", a)
	}
	path, _, err := store.ArtifactPath(context.Background(), a.ID, source.Owner)
	if err != nil {
		t.Fatal(err)
	}
	p, duration, err := probe(context.Background(), c, path, false)
	if err != nil || p.selectedVideo() == nil || p.selectedVideo().Width != 192 || p.selectedVideo().Height != 192 || p.selectedAudio() == nil || duration < 1950 || duration > 2100 {
		t.Fatal("transformed artifact is not playable", p, duration, err)
	}
	if _, err = store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=true WHERE id=$1", audio.ID); err != nil {
		t.Fatal(err)
	}
	recovered := renderJobRequest(t, server, cookie, r, "transformed")
	if recovered.Code != 202 || !strings.Contains(recovered.Body.String(), job.ID) {
		t.Fatal("retained job depends on mutable replacement media", recovered.Code, recovered.Body.String())
	}
	var readers int
	if err := store.DB.QueryRow(context.Background(), "SELECT count(*) FROM source_media_readers").Scan(&readers); err != nil || readers != 0 {
		t.Fatal("worker leaked reader", readers, err)
	}
}

func TestRenderServiceCanvasAndReplacementOwnDecoderBudget(t *testing.T) {
	for _, request := range []ExportRequest{
		{Format: "mp4", Canvas: &edit.Canvas{Width: 4096, Height: 4096}},
		{Format: "mp4", AudioReplace: &edit.AudioReplace{SourceID: "external"}},
		{Format: "mp3", AudioReplace: &edit.AudioReplace{SourceID: "external"}},
	} {
		ctx := withMediaBudget(context.Background(), 3)
		release, err := acquireMediaProcessing(ctx, Source{Path: "tiny", Width: 160, Height: 90}, nil, request)
		if err != nil {
			t.Fatal(err)
		}
		wait, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		other, err := acquireMediaProcessing(wait, Source{}, nil, ExportRequest{Format: "mp3"})
		cancel()
		if other != nil {
			other()
		}
		release()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("multi-input/large-canvas processing bypassed decoder budget", request, err)
		}
	}
}

func TestRenderServiceCollectionReplacementIsolation(t *testing.T) {
	store, _, server, cookie, source, audio := renderServiceFixture(t)
	plan := CollectionPlan{Version: 1, Options: CollectionOptions{AudioReplace: &edit.AudioReplace{SourceID: audio.ID}}, Clips: []CollectionClip{
		{ID: "first", Ranges: []Range{{StartMS: 0, EndMS: 1000}}},
		{ID: "later", Ranges: []Range{{StartMS: 2000, EndMS: 3000}}, Options: CollectionOptions{AudioReplace: &edit.AudioReplace{SourceID: "unowned"}}},
	}}
	request := CollectionRequest{SourceID: source.ID, Plan: plan}
	response := httpCollectionRequest(t, server, cookie, "/api/v1/collections", request, "replacement-batch")
	if response.Code != 404 || !strings.Contains(response.Body.String(), `"clip_id":"later"`) {
		t.Fatal("late replacement failure lost attribution", response.Code, response.Body.String())
	}
	assertHTTPPreflightNoWork(t, store, 0)
	request.Plan.Clips[1].Options = CollectionOptions{ClearAudioReplace: true}
	response = httpCollectionRequest(t, server, cookie, "/api/v1/collections/validate", request, "")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	assertHTTPPreflightNoWork(t, store, 0)
	if _, err := os.Stat(audio.Path); err != nil {
		t.Fatal(err)
	}
}

func TestRenderServiceDiagnosticsKeepPublicCodesAndPrivateCauses(t *testing.T) {
	private := errors.New("private media diagnostics must not reach API messages")
	for _, sample := range []struct {
		err  error
		code string
	}{
		{&mediaerror.Problem{Code: "source_changed", Message: "Source changed; inspect again"}, "source_changed"},
		{&mediaerror.ProcessFailure{Category: "storage_full", Cause: private}, "storage_full"},
		{mediaerror.RenderedFailure(&mediaerror.ProcessFailure{Category: "hdr_conversion_unavailable", Cause: private}, true), "hdr_conversion_unavailable"},
	} {
		converted := mediaExportError(sample.err)
		problem := exportProblem(converted)
		if problem.code != sample.code || strings.Contains(problem.message, private.Error()) {
			t.Fatal("rendering diagnostic lost its public classification", problem)
		}
		if errors.Is(sample.err, private) && !errors.Is(converted, private) {
			t.Fatal("private diagnostic cause was lost")
		}
	}
}
