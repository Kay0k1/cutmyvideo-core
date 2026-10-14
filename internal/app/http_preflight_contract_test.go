package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Validate emitted endpoint responses against the published schema, including
// fresh local tracks/geometry and metadata-only remote deferred checks.
func TestHTTPPreflightPublicContract(t *testing.T) {
	python := os.Getenv("CUTMY_CONTRACT_PYTHON")
	if python == "" {
		python = "python3"
	}
	if output, err := exec.Command(python, "-c", "import yaml, jsonschema").CombinedOutput(); err != nil {
		if os.Getenv("CUTMY_REQUIRE_CONTRACT") == "1" {
			t.Fatalf("install scripts/requirements-contract.txt: %v %s", err, output)
		}
		t.Skip("OpenAPI runtime validation needs scripts/requirements-contract.txt")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" && os.Getenv("CUTMY_REQUIRE_CONTRACT") == "1" {
		t.Fatal("contract checks require isolated TEST_DATABASE_URL")
	}
	_ = mediaConfig(t)
	store, config, server, cookie, owner := httpPreflightFixture(t)
	source := addHTTPPreflightVideo(t, store, config, owner)
	remote := Source{ID: newID("src"), Owner: owner, Title: "Remote metadata", Kind: "platform", Provider: "vimeo", URL: "https://vimeo.com/123456", DurationMS: 10000, Width: 1920, Height: 1080}
	if err := store.AddSource(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	var samples []map[string]any
	record := func(name string, request any, w *httptest.ResponseRecorder) {
		t.Helper()
		var body any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s returned invalid JSON: %v", name, err)
		}
		headers := map[string]string{}
		for key := range w.Header() {
			headers[key] = w.Header().Get(key)
		}
		samples = append(samples, map[string]any{"name": name, "method": "POST", "route": "/api/v1/jobs/preflight", "request": request, "status": w.Code, "headers": headers, "body": body})
	}
	for _, mode := range []string{"separate", "joined"} {
		request := requestFor(source)
		request.OutputMode, request.SubtitleMode = mode, "omit"
		request.Ranges = append(request.Ranges, Range{StartMS: 0, EndMS: 1000, Label: "  another  "})
		w := httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
		httpPreflightReport(t, w)
		record("local-"+mode, request, w)
	}
	request := requestFor(source)
	request.Format, request.CutMode, request.Quality = "mkv", "copy", "best"
	w := httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	httpPreflightReport(t, w)
	record("local-copy-mkv", request, w)
	request = requestFor(remote)
	w = httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	httpPreflightReport(t, w)
	record("remote-metadata", request, w)
	request = requestFor(source)
	request.Format = "mp3"
	w = httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	assertHTTPPreflightError(t, w, 422, "audio_missing")
	record("missing-audio", request, w)
	request = requestFor(source)
	request.Ranges = []Range{{StartMS: 0, EndMS: 5000}}
	w = httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	assertHTTPPreflightError(t, w, 400, "invalid_export")
	record("out-of-bounds", request, w)
	request = requestFor(source)
	w = httpPreflightRequest(t, server.Handler(), context.Background(), nil, request)
	assertHTTPPreflightError(t, w, 401, "session_required")
	record("session-required", request, w)
	request.SourceID = "not-owned"
	w = httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	assertHTTPPreflightError(t, w, 404, "not_found")
	record("not-owned", request, w)
	request = requestFor(source)
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=true WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	w = httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	assertHTTPPreflightError(t, w, 410, "source_removed")
	record("source-removed", request, w)
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET media_removed=false WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	// Real shared origin and strict field guards remain active on the new route.
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/jobs/preflight", bytes.NewReader(data))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://another.example")
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	assertHTTPPreflightError(t, w, 403, "origin_rejected")
	record("cross-origin", request, w)
	var unknown map[string]any
	if err := json.Unmarshal(data, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["unexpected"] = true
	data, err = json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/jobs/preflight", bytes.NewReader(data))
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	assertHTTPPreflightError(t, w, 400, "invalid_request")
	record("unknown-field", unknown, w)
	samples[len(samples)-1]["request_valid"] = false
	assertHTTPPreflightNoWork(t, store, 0)
	store.DB.Close()
	w = httpPreflightRequest(t, server.Handler(), context.Background(), cookie, request)
	assertHTTPPreflightError(t, w, 503, "database_unavailable")
	record("database-unavailable", request, w)
	data, err = json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "preflight-samples.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join("..", "..", "scripts", "check-contract.py")
	if output, err := exec.Command(python, checker, "--samples", path).CombinedOutput(); err != nil {
		t.Fatalf("preflight HTTP/OpenAPI contract: %v\n%s", err, output)
	} else {
		t.Log(strings.TrimSpace(string(output)))
	}
}
