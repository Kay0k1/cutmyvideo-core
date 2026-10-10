package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Validate actual public handlers, not handcrafted copies of response structs.
// Fixture traffic stays local; no media subprocess or live provider is needed.
func TestHTTPPublicContract(t *testing.T) {
	python := os.Getenv("CUTMY_CONTRACT_PYTHON")
	if python == "" {
		python = "python3"
	}
	if out, err := exec.Command(python, "-c", "import yaml, jsonschema").CombinedOutput(); err != nil {
		if os.Getenv("CUTMY_REQUIRE_CONTRACT") == "1" {
			t.Fatalf("install scripts/requirements-contract.txt: %v %s", err, out)
		}
		t.Skip("OpenAPI runtime validation needs scripts/requirements-contract.txt")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" && os.Getenv("CUTMY_REQUIRE_CONTRACT") == "1" {
		t.Fatal("contract checks require isolated TEST_DATABASE_URL")
	}
	store := testStore(t)
	c := Config{
		DataDir: t.TempDir(), PublicOrigin: "https://example.com", SecureCookie: true,
		MaxSourceBytes: 16 << 20, MaxOutputBytes: 10 << 20, MaxFetchBytes: 16 << 20,
		MaxRanges: 12, MaxActiveJobs: 16, MutationsPerMinute: 200,
		MaxRangeMS: 600000, MaxJobMS: 3600000, SourceTimeout: time.Minute,
		SourceTTL: time.Hour, ArtifactTTL: time.Hour,
		MaxStorageBytes: 1 << 30,
	}
	server := NewServer(c, store)
	handler := server.Handler()
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	identity := httptest.NewRequest("GET", "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	var samples []map[string]any
	record := func(name, method, route string, w *httptest.ResponseRecorder) map[string]any {
		headers := map[string]string{}
		for key := range w.Header() {
			headers[key] = w.Header().Get(key)
		}
		var body any = w.Body.String()
		if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: invalid JSON: %v", name, err)
			}
		}
		sample := map[string]any{"name": name, "method": method, "route": route, "status": w.Code, "headers": headers, "body": body}
		samples = append(samples, sample)
		return sample
	}
	request := func(name, method, route, url string, payload any, headers map[string]string, status int) map[string]any {
		var raw []byte
		if payload != nil {
			var err error
			raw, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
		}
		var input io.Reader
		if payload != nil {
			input = bytes.NewReader(raw)
		}
		r := httptest.NewRequest(method, "https://example.com"+url, input)
		r.AddCookie(cookie)
		if payload != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: got %d, want %d: %s", name, w.Code, status, w.Body.String())
		}
		sample := record(name, method, route, w)
		if payload != nil {
			sample["request"] = payload
		}
		return sample
	}
	request("health", "GET", "/healthz", "/healthz", nil, nil, 200)
	request("ready", "GET", "/readyz", "/readyz", nil, nil, 200)
	request("session", "GET", "/api/v1/session", "/api/v1/session", nil, nil, 200)
	request("empty-sources", "GET", "/api/v1/sources", "/api/v1/sources", nil, nil, 200)
	request("empty-jobs", "GET", "/api/v1/jobs", "/api/v1/jobs", nil, nil, 200)

	var thumbnail bytes.Buffer
	if err := png.Encode(&thumbnail, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.DataDir, "fixture.png")
	if err := os.WriteFile(path, thumbnail.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: newID("src"), Owner: owner, Title: "Contract fixture", Kind: "upload", DurationMS: 10000, Path: path, ThumbnailPath: path, Width: 32, Height: 32}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	request("source", "GET", "/api/v1/sources/{id}", "/api/v1/sources/"+source.ID, nil, nil, 200)
	request("owned-sources", "GET", "/api/v1/sources", "/api/v1/sources", nil, nil, 200)
	// More legacy directories than three admission attempts can account for.
	// Exercise genuine partial accounting through each public write path, with
	// no encoder or remote request and no test-only error injected into handlers.
	for i := range storageProgressBatches * 4 {
		if err := os.MkdirAll(filepath.Join(c.DataDir, "work", fmt.Sprintf("legacy-%03d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	warming := request("source-storage-initializing", "POST", "/api/v1/sources", "/api/v1/sources", map[string]any{"url": "https://cdn.example.com/fixture.mp4"}, nil, 503)
	assertInitializing := func(sample map[string]any) {
		t.Helper()
		if sample["body"].(map[string]any)["error"].(map[string]any)["code"] != "storage_initializing" || sample["headers"].(map[string]string)["Retry-After"] != "2" {
			t.Fatal("partial accounting did not return its retryable contract", sample)
		}
	}
	assertInitializing(warming)
	// Database readiness lets the dependent worker start during accounting.
	request("ready-during-storage-warmup", "GET", "/readyz", "/readyz", nil, nil, 200)
	upload := httptest.NewRequest("POST", "https://example.com/api/v1/uploads", nil)
	upload.AddCookie(cookie)
	upload.Header.Set("Content-Type", "multipart/form-data; boundary=unread")
	uploadBody := &contractUnreadBody{}
	upload.Body = uploadBody
	warmingUpload := httptest.NewRecorder()
	handler.ServeHTTP(warmingUpload, upload)
	if warmingUpload.Code != 503 || uploadBody.reads != 0 {
		t.Fatal("partial accounting read the upload body or reported the wrong status", warmingUpload.Code, uploadBody.reads)
	}
	assertInitializing(record("upload-storage-initializing", "POST", "/api/v1/uploads", warmingUpload))
	assertInitializing(request("preview-storage-initializing", "GET", "/api/v1/sources/{id}/preview", "/api/v1/sources/"+source.ID+"/preview?start_ms=0", nil, nil, 503))
	_, reserved := ledgerBytes(t, store)
	if reserved != 0 {
		t.Fatal("rejected warming admissions retained storage reservations", reserved)
	}
	for attempts := 0; ; attempts++ {
		err := store.bootstrapStorage(context.Background(), c)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrStorageInitializing) || attempts > 10 {
			t.Fatal("HTTP fixture could not finish persisted accounting", err)
		}
	}
	for _, kind := range []string{"direct", "platform", "youtube"} {
		variant := Source{ID: newID("src"), Owner: owner, Title: "Metadata variant", Kind: kind, DurationMS: 10000}
		if kind == "direct" {
			variant.Path = path
			variant.URL = "https://cdn.example.com/fixture.mp4"
		} else if kind == "platform" {
			variant.Provider, variant.ProviderID = "vimeo", "123456"
			variant.URL = "https://vimeo.com/123456"
		} else {
			variant.Provider, variant.ProviderID = "youtube", "abcdefghijk"
			variant.URL = "https://www.youtube.com/watch?v=abcdefghijk"
		}
		if err := store.AddSource(context.Background(), variant); err != nil {
			t.Fatal(err)
		}
		request(kind+"-metadata", "GET", "/api/v1/sources/{id}", "/api/v1/sources/"+variant.ID, nil, nil, 200)
	}
	for _, suffix := range []string{"media", "thumbnail"} {
		route := "/api/v1/sources/{id}/" + suffix
		url := "/api/v1/sources/" + source.ID + "/" + suffix
		request(suffix+"-bytes", "GET", route, url, nil, nil, 200)
		request(suffix+"-range", "GET", route, url, nil, map[string]string{"Range": "bytes=0-3"}, 206)
		sample := request(suffix+"-unsatisfiable", "GET", route, url, nil, map[string]string{"Range": "bytes=999999-"}, 416)
		if sample["headers"].(map[string]string)["Content-Range"] == "" {
			t.Fatal("unsatisfiable range lacks Content-Range")
		}
		request(suffix+"-malformed", "GET", route, url, nil, map[string]string{"Range": "bytes=broken"}, 416)
		request(suffix+"-precondition", "GET", route, url, nil, map[string]string{"If-Match": `"does-not-match"`}, 412)
		request(suffix+"-multiple-ranges", "GET", route, url, nil, map[string]string{"Range": "bytes=0-1,4-5"}, 206)
		r := httptest.NewRequest("GET", "https://example.com"+url, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		request(suffix+"-conditional", "GET", route, url, nil, map[string]string{"If-None-Match": w.Header().Get("ETag")}, 304)
		request(suffix+"-head", "HEAD", route, url, nil, nil, 200)
	}
	// The preview serving helper is shared by freshly rendered and cached files.
	// Verify its Range/header contract without starting an encoder.
	for _, rangeValue := range []string{"", "bytes=0-3", "bytes=999999-", "bytes=broken"} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Range", rangeValue)
		w := httptest.NewRecorder()
		servePreview(w, r, file, Range{EndMS: 10000}, "hit")
		file.Close()
		record("preview-"+rangeValue, "GET", "/api/v1/sources/{id}/preview", w)["parameters"] = map[string]any{"query:start_ms": 0, "query:priority": "background"}
	}
	for _, condition := range []struct {
		name, value string
		status      int
	}{{"If-Match", `"does-not-match"`, 412}, {"If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), 304}} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set(condition.name, condition.value)
		w := httptest.NewRecorder()
		servePreview(w, r, file, Range{EndMS: 10000}, "hit")
		file.Close()
		if w.Code != condition.status || w.Body.Len() != 0 {
			t.Fatal("preview precondition contract failed", w.Code, w.Body.String())
		}
		record("preview-"+condition.name, "GET", "/api/v1/sources/{id}/preview", w)
	}
	request("invalid-preview", "GET", "/api/v1/sources/{id}/preview", "/api/v1/sources/"+source.ID+"/preview?start_ms=-1&priority=background", nil, nil, 400)["parameters"] = map[string]any{"query:start_ms": -1, "query:priority": "background"}
	samples[len(samples)-1]["invalid_parameters"] = []string{"query:start_ms"}
	invalidURL := map[string]any{"url": "https://cdn.example.com/" + strings.Repeat("界", 2731)}
	request("utf8-url-limit", "POST", "/api/v1/sources", "/api/v1/sources", invalidURL, nil, 400)["request_valid"] = false
	request("unknown-request-field", "POST", "/api/v1/sources", "/api/v1/sources", map[string]any{"url": "https://cdn.example.com/fixture.mp4", "extra": true}, nil, 400)["request_valid"] = false
	jobRequest := map[string]any{"source_id": source.ID, "ranges": []any{map[string]any{"start_ms": 1000, "end_ms": 3000, "label": strings.Repeat("界", 66)}}, "format": "mp4", "quality": "best", "cut_mode": "accurate"}
	oversizeKey := strings.Repeat("界", 43)
	request("utf8-idempotency-limit", "POST", "/api/v1/jobs", "/api/v1/jobs", jobRequest, map[string]string{"Idempotency-Key": oversizeKey}, 400)["parameters"] = map[string]any{"header:Idempotency-Key": oversizeKey}
	samples[len(samples)-1]["invalid_parameters"] = []string{"header:Idempotency-Key"}
	key := strings.Repeat("界", 42)
	accepted := request("job-submission", "POST", "/api/v1/jobs", "/api/v1/jobs", jobRequest, map[string]string{"Idempotency-Key": key}, 202)
	accepted["parameters"] = map[string]any{"header:Idempotency-Key": key}
	jobID := accepted["body"].(map[string]any)["id"].(string)
	replayed := request("job-replay", "POST", "/api/v1/jobs", "/api/v1/jobs", jobRequest, map[string]string{"Idempotency-Key": key}, 202)
	if replayed["body"].(map[string]any)["id"] != jobID {
		t.Fatal("idempotency replay created another job")
	}
	request("queued-job", "GET", "/api/v1/jobs/{id}", "/api/v1/jobs/"+jobID, nil, nil, 200)
	request("job-history", "GET", "/api/v1/jobs", "/api/v1/jobs", nil, nil, 200)
	job, token, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: newID("artifact"), Filename: "fixture.mp4", SizeBytes: int64(thumbnail.Len()), ActualStartMS: 1000, ActualEndMS: 3000}
	if err := store.AddArtifact(context.Background(), owner, job.ID, path, token, artifact); err != nil {
		t.Fatal(err)
	}
	job.Items[0].Status, job.Items[0].Artifact = "succeeded", &artifact
	job.Status, job.Stage = "succeeded", "finished"
	if err := store.SaveJob(context.Background(), job, token); err != nil {
		t.Fatal(err)
	}
	request("succeeded-job", "GET", "/api/v1/jobs/{id}", "/api/v1/jobs/"+jobID, nil, nil, 200)
	request("retained-history", "GET", "/api/v1/jobs", "/api/v1/jobs", nil, nil, 200)
	failed, err := store.CreateJob(context.Background(), owner, requestFor(source), "diagnostic-fixture")
	if err != nil {
		t.Fatal(err)
	}
	failed, failedToken, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failed.Items[0].Status, failed.Items[0].ErrorCode, failed.Items[0].Message = "failed", "future_diagnostic", "Safe generic diagnostic"
	failed.Status, failed.Stage = "failed", "finished"
	if err := store.SaveJob(context.Background(), failed, failedToken); err != nil {
		t.Fatal(err)
	}
	request("failed-job-future-code", "GET", "/api/v1/jobs/{id}", "/api/v1/jobs/"+failed.ID, nil, nil, 200)
	cancelled, err := store.CreateJob(context.Background(), owner, requestFor(source), "cancel-fixture")
	if err != nil {
		t.Fatal(err)
	}
	request("cancelled-job", "POST", "/api/v1/jobs/{id}/cancel", "/api/v1/jobs/"+cancelled.ID+"/cancel", nil, nil, 200)
	for _, pair := range []struct {
		value  string
		status int
	}{{"", 200}, {"bytes=0-3", 206}, {"bytes=999999-", 416}, {"bytes=broken", 416}} {
		request("download-"+pair.value, "GET", "/api/v1/artifacts/{id}/download", "/api/v1/artifacts/"+artifact.ID+"/download", nil, map[string]string{"Range": pair.value}, pair.status)
	}
	request("download-precondition", "GET", "/api/v1/artifacts/{id}/download", "/api/v1/artifacts/"+artifact.ID+"/download", nil, map[string]string{"If-Match": `"does-not-match"`}, 412)
	request("download-conditional", "GET", "/api/v1/artifacts/{id}/download", "/api/v1/artifacts/"+artifact.ID+"/download", nil, map[string]string{"If-Modified-Since": time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}, 304)
	request("not-found", "GET", "/api/v1/sources/{id}", "/api/v1/sources/src_absent", nil, nil, 404)
	r := httptest.NewRequest("GET", "/api/v1/sources", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unauthorized contract fixture was not rejected")
	}
	record("session-required", "GET", "/api/v1/sources", w)
	request("delete", "DELETE", "/api/v1/sources/{id}", "/api/v1/sources/"+source.ID, nil, nil, 204)
	// Closed pool is a real driver failure; assert the bounded API vocabulary.
	store.DB.Close()
	unavailable := request("database-unavailable", "GET", "/api/v1/sources", "/api/v1/sources", nil, nil, 503)
	if unavailable["body"].(map[string]any)["error"].(map[string]any)["code"] != "database_unavailable" {
		t.Fatal("database failure did not use stable code")
	}
	request("not-ready", "GET", "/readyz", "/readyz", nil, nil, 503)

	data, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	samplesPath := filepath.Join(t.TempDir(), "samples.json")
	if err := os.WriteFile(samplesPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join("..", "..", "scripts", "check-contract.py")
	if output, err := exec.Command(python, checker, "--samples", samplesPath).CombinedOutput(); err != nil {
		t.Fatalf("HTTP/OpenAPI contract: %v\n%s", err, output)
	} else {
		t.Log(strings.TrimSpace(string(output)))
	}
}

type contractUnreadBody struct{ reads int }

func (b *contractUnreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("rejected upload body must remain unread")
}
func (*contractUnreadBody) Close() error { return nil }
