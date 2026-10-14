package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func httpPreflightFixture(t *testing.T) (*Store, Config, *Server, *http.Cookie, string) {
	t.Helper()
	store := testStore(t)
	config := Config{DataDir: t.TempDir(), PublicOrigin: "https://example.com", FFmpeg: "ffmpeg", FFprobe: "ffprobe",
		MaxSourceBytes: 16 << 20, MaxOutputBytes: 16 << 20, MaxRanges: 12, MaxRangeMS: 600000, MaxJobMS: 3600000,
		SourceTimeout: 10 * time.Second, MaxActiveJobs: 32, MutationsPerMinute: 200, FFmpegThreads: 1}
	server := NewServer(config, store)
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	identity := httptest.NewRequest(http.MethodGet, "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	return store, config, server, cookie, owner
}

func httpPreflightRequest(t *testing.T, handler http.Handler, ctx context.Context, cookie *http.Cookie, request ExportRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/jobs/preflight", bytes.NewReader(data)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func httpPreflightReport(t *testing.T, w *httptest.ResponseRecorder) PreflightResult {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("preflight returned %d: %s", w.Code, w.Body.String())
	}
	var result PreflightResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Valid || result.Version != 1 {
		t.Fatalf("invalid preflight report: %s (%v)", w.Body.String(), err)
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("inspection response is not private/no-store")
	}
	return result
}

func assertHTTPPreflightError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response struct{ Error struct{ Code string } }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != status || response.Error.Code != code {
		t.Fatalf("got %d %s; want %d %s (%v)", w.Code, w.Body.String(), status, code, err)
	}
}

func assertHTTPPreflightNoWork(t *testing.T, store *Store, reservationCount int) {
	t.Helper()
	var jobs, artifacts, reservations, readers int
	err := store.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM jobs),(SELECT count(*) FROM artifacts),(SELECT count(*) FROM storage_reservations),(SELECT count(*) FROM source_media_readers)`).Scan(&jobs, &artifacts, &reservations, &readers)
	if err != nil || jobs != 0 || artifacts != 0 || reservations != reservationCount || readers != 0 {
		t.Fatalf("preflight retained execution state: jobs=%d artifacts=%d reservations=%d readers=%d err=%v", jobs, artifacts, reservations, readers, err)
	}
}

func preflightCheckStatus(result PreflightResult, name string) string {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}

func TestHTTPPreflightRemoteMetadataAndGuards(t *testing.T) {
	store, config, server, cookie, owner := httpPreflightFixture(t)
	// A private extracted-looking address must never be resolved or returned.
	source := Source{ID: newID("src"), Owner: owner, Title: "Remote fixture", Kind: "platform", Provider: "vimeo", URL: "https://127.0.0.1:1/private?credential=never-expose", DurationMS: 10000, Width: 1920, Height: 1080}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	server.Config.FFmpeg, server.Config.FFprobe, server.Config.YTDLP = "/unavailable", "/unavailable", "/unavailable"
	request := requestFor(source)
	request.OutputMode, request.SubtitleMode = "separate", "omit"
	request.Ranges[0].Label = "  First  "
	request.Ranges = append(request.Ranges, Range{StartMS: 2000, EndMS: 4000, Label: "Second"})
	result := httpPreflightReport(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request))
	if result.RequestedMS != 4000 || result.OutputCount != 2 || result.Request.SourceID != source.ID || result.Request.Ranges[0].Label != "First" || result.Request.OutputMode != "" || result.Request.SubtitleMode != "" || len(result.TrackManifest) != 0 || result.Video != nil {
		t.Fatalf("metadata-only request normalization changed: %+v", result)
	}
	for _, check := range []string{"source_media", "tracks", "video_geometry", "tool_capabilities", "media_decode", "output_size", "storage", "publication"} {
		if preflightCheckStatus(result, check) != "deferred" {
			t.Fatalf("remote report incorrectly verified %s: %+v", check, result.Checks)
		}
	}
	response := httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	if strings.Contains(response.Body.String(), "127.0.0.1") || strings.Contains(response.Body.String(), "credential") || strings.Contains(response.Body.String(), config.DataDir) {
		t.Fatal("preflight exposed private source data")
	}
	assertHTTPPreflightNoWork(t, store, 0)
	request.OutputMode = "joined"
	if result := httpPreflightReport(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)); result.OutputCount != 1 {
		t.Fatal("joined metadata preflight did not report one output")
	}
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), nil, request), 401, "session_required")
	otherCookie := &http.Cookie{Name: cookie.Name, Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), otherCookie, request), 404, "not_found")
	badRequest := request
	badRequest.Ranges = []Range{{StartMS: 0, EndMS: 11000}}
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, badRequest), 400, "invalid_export")
	badRequest = request
	audio := 0
	badRequest.AudioTrack = &audio
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, badRequest), 400, "invalid_export")
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=true WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request), 410, "source_removed")
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=false WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	// Exhausting inspection allowance does not consume the new-export counter.
	for i := 0; i < 30; i++ {
		server.allow("preflight:"+owner, 30)
	}
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request), 429, "rate_limit")
	for i := 0; i < 10; i++ {
		if !server.allow("export:"+owner, 10) {
			t.Fatal("job preflight consumed new-export allowance")
		}
	}
	assertHTTPPreflightNoWork(t, store, 0)
}

func addHTTPPreflightVideo(t *testing.T, store *Store, config Config, owner string) Source {
	t.Helper()
	path := filepath.Join(config.DataDir, "preflight-source.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mcRun(t, ctx, config, "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25", "-t", "4", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", path)
	source := Source{ID: newID("src"), Owner: owner, Title: "Native preflight", Kind: "upload", Path: path, DurationMS: 4000, Width: 160, Height: 96}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestHTTPPreflightLocalReadOnlyAndAdmissionCleanup(t *testing.T) {
	_ = mediaConfig(t)
	store, config, server, cookie, owner := httpPreflightFixture(t)
	source := addHTTPPreflightVideo(t, store, config, owner)
	request := requestFor(source)
	request.OutputMode = "joined"
	request.Ranges = append(request.Ranges, request.Ranges[0])
	before, err := os.ReadDir(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w := httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
		result := httpPreflightReport(t, w)
		if result.RequestedMS != 4000 || result.OutputCount != 1 || result.Source.DurationMS != 4000 || result.Video == nil || result.Video.Width != 160 || result.Video.Height != 96 || len(result.TrackManifest) != 1 || !result.TrackManifest[0].Included {
			t.Fatalf("fresh local report lost geometry or track selection: %+v", result)
		}
		if strings.Contains(w.Body.String(), source.Path) || strings.Contains(w.Body.String(), config.DataDir) {
			t.Fatal("local preflight leaked media path")
		}
		for _, name := range []string{"storage", "publication", "output_size", "media_decode"} {
			if preflightCheckStatus(result, name) != "deferred" {
				t.Fatalf("local report promised %s: %+v", name, result.Checks)
			}
		}
		assertHTTPPreflightNoWork(t, store, 0)
	}
	request.Format = "mp3"
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request), 422, "audio_missing")
	assertHTTPPreflightNoWork(t, store, 0)
	request.Format = "mp4"
	audio := 99
	request.AudioTrack = &audio
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request), 422, "audio_track_unavailable")
	assertHTTPPreflightNoWork(t, store, 0)
	request.AudioTrack = nil
	server.Config.FFmpeg = filepath.Join(config.DataDir, "missing-tool")
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request), 422, "tool_unavailable")
	assertHTTPPreflightNoWork(t, store, 0)
	after, err := os.ReadDir(config.DataDir)
	if err != nil || len(after) != len(before) {
		t.Fatalf("inspection created output/workspace files: before=%d after=%d err=%v", len(before), len(after), err)
	}
	for i := range after {
		if after[i].Name() != before[i].Name() {
			t.Fatal("inspection changed file inventory")
		}
	}
}

func TestHTTPPreflightCancellationReleasesLeases(t *testing.T) {
	store, _, server, cookie, owner := httpPreflightFixture(t)
	source := storedSource(t, store, owner)
	server.Config.FFprobe, server.Config.FFmpeg = "/must-not-start", "/must-not-start"
	// Reader admission is separately bounded before expensive inspection.
	if _, err := store.DB.Exec(context.Background(), `INSERT INTO source_media_readers(id,source_id,expires_at) SELECT 'preflight-reader-'||i,$1,now()+interval '1 minute' FROM generate_series(1,128) i`, source.ID); err != nil {
		t.Fatal(err)
	}
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, requestFor(source)), 503, "server_busy")
	if _, err := store.DB.Exec(context.Background(), "DELETE FROM source_media_readers WHERE id LIKE 'preflight-reader-%'"); err != nil {
		t.Fatal(err)
	}
	assertHTTPPreflightNoWork(t, store, 0)
	// Occupy the same cross-process inspection pool used by previews/tracks.
	// The pending preflight must hold only a renewable source-reader lease.
	if _, err := store.DB.Exec(context.Background(), `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id,token) VALUES('inspection-blocker',$1,'preview',0,now()+interval '1 minute',$2,'inspection-full:blocker')`, owner, source.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- httpPreflightRequest(t, server.Handler(), ctx, cookie, requestFor(source)) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var readers int
		if err := store.DB.QueryRow(context.Background(), "SELECT count(*) FROM source_media_readers").Scan(&readers); err != nil {
			t.Fatal(err)
		}
		if readers == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending preflight never acquired protected source read")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case response := <-done:
		if response.Body.Len() != 0 {
			t.Fatal("canceled inspection emitted a success/error body", response.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop pending admission")
	}
	assertHTTPPreflightNoWork(t, store, 1)
	// The earlier caller's reservation must remain untouched.
	var token string
	if err := store.DB.QueryRow(context.Background(), "SELECT token FROM storage_reservations WHERE id='inspection-blocker'").Scan(&token); err != nil || token != "inspection-full:blocker" {
		t.Fatal("cancel cleanup removed another admission", token, err)
	}
	server.Config.SourceTimeout = 60 * time.Millisecond
	assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, requestFor(source)), 504, "source_timeout")
	assertHTTPPreflightNoWork(t, store, 1)
	// Loss of the server's protected reader context is distinct from a client
	// disconnect and must not emit a successful/unsupported-media response.
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/preflight", nil)
	response := httptest.NewRecorder()
	writePreflightError(response, request, ctx, context.Canceled)
	assertHTTPPreflightError(t, response, 503, "server_busy")
}
