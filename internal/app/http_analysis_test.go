package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func newInterfacesAnalysisFixture(t *testing.T) (*Store, Config, *Server, *http.Cookie, Source) {
	t.Helper()
	_ = mediaConfig(t)
	store, c, server, cookie, owner := httpPreflightFixture(t)
	path := filepath.Join(c.DataDir, "analysis.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "testsrc2=s=160x90:r=25:d=4", "-f", "lavfi", "-i", `aevalsrc=if(between(t\,1\,3)\,0.3*sin(2*PI*997*t)\,0):s=48000:d=4`, "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-threads", "1", "-c:a", "pcm_s16le", path)
	source := Source{ID: newID("src"), Owner: owner, Title: "Analysis fixture", Kind: "upload", Path: path, DurationMS: 4000, Width: 160, Height: 90}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	return store, c, server, cookie, source
}

func TestNewInterfacesHTTPActualAnalysisOwnerIsolationAndCleanup(t *testing.T) {
	store, c, server, cookie, source := newInterfacesAnalysisFixture(t)
	before, err := os.ReadDir(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"silence", "video", "loudness"} {
		path := "/api/v1/sources/" + source.ID + "/analysis/" + kind
		other := &http.Cookie{Name: cookie.Name, Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
		assertHTTPPreflightError(t, newInterfacesHTTPRequest(server.Handler(), context.Background(), other, path, `{}`), 404, "not_found")
		response := newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, path, `{"start_ms":0,"end_ms":4000}`)
		if response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(kind, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), c.DataDir) || strings.Contains(response.Body.String(), source.Path) {
			t.Fatal("analysis exposed private local paths")
		}
		switch kind {
		case "silence":
			var result localmedia.SilenceResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Version != 1 || len(result.Silence) != 2 || result.Silence[0].StartMS != 0 || result.Silence[0].EndMS < 990 || result.Silence[0].EndMS > 1010 || result.AnalyzedEndMS != 4000 {
				t.Fatal(result, err)
			}
		case "video":
			var result localmedia.VideoAnalysisResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Version != 1 || result.AnalyzedEndMS != 3960 || result.Scenes == nil || result.Black == nil {
				t.Fatal(result, err)
			}
		case "loudness":
			var result localmedia.LoudnessResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Version != 1 || result.IntegratedLUFS == nil || result.TruePeakDBTP == nil || result.AnalyzedEndMS < 3990 || result.AnalyzedEndMS > 4000 {
				t.Fatal(result, err)
			}
		}
		assertHTTPPreflightNoWork(t, store, 0)
	}
	// A default end is resolved once against admitted source metadata. A longer
	// freshly probed container must not expand the request behind that limit.
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET duration_ms=1000 WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"silence", "video", "loudness"} {
		response := newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, "/api/v1/sources/"+source.ID+"/analysis/"+kind, `{}`)
		var result struct {
			EndMS int64 `json:"end_ms"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || result.EndMS != 1000 {
			t.Fatal("analysis expanded its admitted interval", kind, response.Code, response.Body.String(), err)
		}
		assertHTTPPreflightNoWork(t, store, 0)
	}
	after, err := os.ReadDir(c.DataDir)
	if err != nil || len(before) != len(after) {
		t.Fatal("analysis created output files", before, after, err)
	}
	if _, err := os.Stat(source.Path); err != nil {
		t.Fatal("analysis removed source", err)
	}
}

func TestNewInterfacesHTTPAnalysisRemoteRemovedAndRateGuards(t *testing.T) {
	store, _, server, cookie, owner := httpPreflightFixture(t)
	remote := Source{ID: newID("src"), Owner: owner, Title: "Remote", Kind: "platform", Provider: "vimeo", URL: "https://127.0.0.1:1/private?credential=never-expose", DurationMS: 4000}
	if err := store.AddSource(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	server.Config.FFmpeg, server.Config.FFprobe, server.Config.YTDLP = "/unavailable", "/unavailable", "/unavailable"
	for _, kind := range []string{"silence", "video", "loudness"} {
		response := newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, "/api/v1/sources/"+remote.ID+"/analysis/"+kind, `{}`)
		assertHTTPPreflightError(t, response, 422, "local_source_required")
		if strings.Contains(response.Body.String(), "127.0.0.1") || strings.Contains(response.Body.String(), "credential") {
			t.Fatal("provider address leaked")
		}
	}
	source := storedSource(t, store, owner)
	path := "/api/v1/sources/" + source.ID + "/analysis/silence"
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, path, `{"start_ms":0,"end_ms":11000}`), 400, "invalid_analysis")
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=true WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, path, `{}`), 410, "source_removed")
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=false WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		server.allow("analysis:"+owner, 6)
	}
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, path, `{}`), 429, "rate_limit")
	assertHTTPPreflightNoWork(t, store, 0)
}

func TestNewInterfacesHTTPAnalysisAdmissionAndCancellationRelease(t *testing.T) {
	store, _, server, cookie, owner := httpPreflightFixture(t)
	source := storedSource(t, store, owner)
	conservative := source
	conservative.Width, conservative.Height = 0, 0
	id := newID("inspection")
	if err := store.reserveSourceInspection(context.Background(), conservative, id, time.Minute); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/sources/" + source.ID + "/analysis/silence"
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(server.Handler(), context.Background(), cookie, path, `{}`), 503, "server_busy")
	assertHTTPPreflightNoWork(t, store, 1)
	server.releasePreviewAdmission(source, id, false)
	assertHTTPPreflightNoWork(t, store, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
	request.SetPathValue("id", source.ID)
	response := httptest.NewRecorder()
	called := false
	server.analyzeLocalSource(response, request, owner, 0, 1000, func(protected context.Context, _ string, end int64) (any, error) {
		called = true
		if end != 1000 {
			t.Fatal("analysis lost its admitted interval", end)
		}
		var reservations, readers int
		if err := store.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM storage_reservations WHERE kind='preview'),(SELECT count(*) FROM source_media_readers)`).Scan(&reservations, &readers); err != nil || reservations != 1 || readers != 1 {
			t.Fatal("analysis ran without both leases", reservations, readers, err)
		}
		cancel()
		<-protected.Done()
		return nil, protected.Err()
	})
	if !called || !errors.Is(ctx.Err(), context.Canceled) || response.Body.Len() != 0 {
		t.Fatal("cancelled analysis certified a result", called, response.Body.String())
	}
	assertHTTPPreflightNoWork(t, store, 0)
}
