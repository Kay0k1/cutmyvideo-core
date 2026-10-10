package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This service acceptance test uses no shell scripts or platform-specific
// process fixtures. Native release jobs require its prerequisites explicitly.
func TestNativeServiceUploadExportAndDownload(t *testing.T) {
	if os.Getenv("CUTMY_REQUIRE_NATIVE_SERVICE") == "1" {
		if os.Getenv("TEST_DATABASE_URL") == "" {
			t.Fatal("native service acceptance requires TEST_DATABASE_URL")
		}
		for _, tool := range []string{"ffmpeg", "ffprobe"} {
			if _, err := exec.LookPath(tool); err != nil {
				t.Fatalf("native service acceptance requires %s: %v", tool, err)
			}
		}
	}
	s := testStore(t)
	c := mediaConfig(t)
	base := c.DataDir
	c.DataDir = filepath.Join(base, "new", "native", "media")
	c.WorkerHealthPath = filepath.Join(base, "worker-health")
	c.WorkerConcurrency, c.FFmpegThreads = 1, 1
	c.SourceTimeout, c.JobTimeout = 20*time.Second, 30*time.Second
	c.SourceTTL, c.ArtifactTTL, c.StorageWaitTimeout = time.Hour, time.Hour, time.Minute
	c.MaxStorageBytes, c.MaxOwnerBytes, c.MaxActiveJobs, c.MutationsPerMinute = 1<<30, 1<<30, 32, 100
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fixture := filepath.Join(base, "synthetic.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-movflags", "+faststart", fixture); err != nil {
		t.Fatal(err)
	}
	media, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(c, s).Handler())
	defer server.Close()
	client := server.Client()
	client.Timeout = 30 * time.Second
	var cookie *http.Cookie
	request := func(method, path string, body io.Reader, headers http.Header, session *http.Cookie, expected int) ([]byte, http.Header) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		r.Header = headers.Clone()
		if r.Header == nil {
			r.Header = make(http.Header)
		}
		if session != nil {
			r.AddCookie(session)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != expected {
			t.Fatalf("%s %s returned %d, expected %d: %s (%v)", method, path, response.StatusCode, expected, data, err)
		}
		if path == "/api/v1/session" {
			for _, session := range response.Cookies() {
				if session.Name == "cutmy_session" {
					cookie = session
				}
			}
		}
		return data, response.Header
	}
	request(http.MethodGet, "/api/v1/session", nil, nil, nil, http.StatusOK)
	if cookie == nil {
		t.Fatal("native service did not create an owner session")
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	part, err := form.CreateFormFile("file", "synthetic.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(media); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	uploaded, _ := request(http.MethodPost, "/api/v1/uploads", &upload, http.Header{"Content-Type": {form.FormDataContentType()}}, cookie, http.StatusCreated)
	var source Source
	if err := json.Unmarshal(uploaded, &source); err != nil || source.ID == "" || source.PreviewURL == nil {
		t.Fatalf("upload returned an incomplete source: %s (%v)", uploaded, err)
	}
	retained, _ := request(http.MethodGet, *source.PreviewURL, nil, nil, cookie, http.StatusOK)
	if !bytes.Equal(retained, media) {
		t.Fatal("acknowledged upload changed its bytes")
	}
	previewURL := "/api/v1/sources/" + source.ID + "/preview?start_ms=1000"
	preview, previewHeaders := request(http.MethodGet, previewURL, nil, nil, cookie, http.StatusOK)
	if len(preview) == 0 || previewHeaders.Get("Content-Type") != "video/mp4" || previewHeaders.Get("X-Preview-Cache") != "miss" {
		t.Fatal("native window preview did not render a private MP4")
	}
	cached, cacheHeaders := request(http.MethodGet, previewURL, nil, nil, cookie, http.StatusOK)
	if !bytes.Equal(cached, preview) || cacheHeaders.Get("X-Preview-Cache") != "hit" {
		t.Fatal("native service did not recover its completed preview cache")
	}
	export := ExportRequest{SourceID: source.ID, Ranges: []Range{{StartMS: 1000, EndMS: 3000, Label: "Native export"}}, Format: "mp4", Quality: "720p", CutMode: "accurate"}
	encoded, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _ := request(http.MethodPost, "/api/v1/jobs", bytes.NewReader(encoded), http.Header{"Content-Type": {"application/json"}}, cookie, http.StatusAccepted)
	var job Job
	if err := json.Unmarshal(accepted, &job); err != nil || job.Status != "queued" || len(job.Items) != 1 {
		t.Fatalf("native service did not queue the export: %s (%v)", accepted, err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- RunWorker(workerCtx, c, s) }()
	t.Cleanup(func() {
		stopWorker()
		select {
		case err := <-workerDone:
			if err != nil {
				t.Errorf("native worker shutdown failed: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("native worker did not stop within its cleanup budget")
		}
	})
	jobURL := "/api/v1/jobs/" + job.ID
	for {
		state, _ := request(http.MethodGet, jobURL, nil, nil, cookie, http.StatusOK)
		if err := json.Unmarshal(state, &job); err != nil {
			t.Fatal(err)
		}
		if job.Status == "succeeded" {
			break
		}
		if job.Status != "queued" && job.Status != "running" {
			t.Fatalf("native worker did not finish the accepted export: %s", state)
		}
		select {
		case <-ctx.Done():
			t.Fatal("native service export timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if job.Items[0].Status != "succeeded" || job.Items[0].Artifact == nil || job.Items[0].Artifact.SizeBytes <= 0 {
		t.Fatalf("succeeded export has no complete result: %+v", job.Items)
	}
	artifact := job.Items[0].Artifact
	output, outputHeaders := request(http.MethodGet, artifact.DownloadURL, nil, nil, cookie, http.StatusOK)
	if int64(len(output)) != artifact.SizeBytes || outputHeaders.Get("Content-Disposition") == "" {
		t.Fatal("native download differs from the acknowledged artifact")
	}
	partial, _ := request(http.MethodGet, artifact.DownloadURL, nil, http.Header{"Range": {"bytes=0-31"}}, cookie, http.StatusPartialContent)
	if !bytes.Equal(partial, output[:32]) {
		t.Fatal("native download lost byte range support")
	}
	foreign := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
	for _, path := range []string{jobURL, artifact.DownloadURL, *source.PreviewURL, previewURL} {
		request(http.MethodGet, path, nil, nil, foreign, http.StatusNotFound)
	}
	downloaded := filepath.Join(base, "downloaded.mp4")
	if err := os.WriteFile(downloaded, output, 0600); err != nil {
		t.Fatal(err)
	}
	mediaInfo, duration, err := probe(ctx, c, downloaded, false)
	if err != nil || duration < 1900 || duration > 2100 || len(mediaInfo.Streams) != 2 {
		t.Fatalf("downloaded media cannot be probed correctly: %+v, duration=%d, err=%v", mediaInfo, duration, err)
	}
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", downloaded, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-"); err != nil {
		t.Fatal("native downloaded output cannot be decoded", err)
	}
}
