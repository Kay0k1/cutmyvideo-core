package app

// Supplemental in-process pipeline witness. Only the fixed fixture transport,
// extractor JSON adapter and P0/P1 config adapter differ from ordinary runtime.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type corePerfMixedTransport func(*http.Request) (*http.Response, error)

func (f corePerfMixedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type corePerfMixedNetwork struct {
	Path           string `json:"path"`
	StartNS, EndNS int64
	Bytes          int
}
type corePerfMixedState struct {
	mu       sync.Mutex
	fixture  string
	metadata []byte
	requests []corePerfMixedNetwork
}

var corePerfMixedFixture *corePerfMixedState

func corePerfMixedHTTPClient() *http.Client {
	state := corePerfMixedFixture
	return &http.Client{Timeout: 30 * time.Second, Transport: corePerfMixedTransport(func(r *http.Request) (*http.Response, error) {
		if state == nil || r.URL.Scheme != "https" || r.URL.Host != "media.example" || r.URL.RawQuery != "" {
			return nil, errors.New("outside fixed mixed fixture")
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || filepath.Base(name) != name || strings.Contains(name, "..") {
			return nil, errors.New("outside fixed mixed fixture path")
		}
		data, err := os.ReadFile(filepath.Join(state.fixture, name))
		if err != nil {
			return nil, err
		}
		start := time.Now().UnixNano()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-time.After(250 * time.Millisecond):
		}
		end := time.Now().UnixNano()
		state.mu.Lock()
		state.requests = append(state.requests, corePerfMixedNetwork{Path: name, StartNS: start, EndNS: end, Bytes: len(data)})
		state.mu.Unlock()
		return &http.Response{StatusCode: 200, ContentLength: int64(len(data)), Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
}

func platformMetadata(ctx context.Context, c Config, raw string, g *networkGuard) (platformInfo, error) {
	if ctx.Err() != nil {
		return platformInfo{}, ctx.Err()
	}
	if raw != "https://www.youtube.com/watch?v=SlowNet0001" && raw != "https://www.youtube.com/watch?v=SlowNet0002" {
		return platformInfo{}, errors.New("outside fixed mixed metadata")
	}
	var info platformInfo
	if corePerfMixedFixture == nil {
		return info, errors.New("mixed fixture not initialized")
	}
	if err := json.Unmarshal(corePerfMixedFixture.metadata, &info); err != nil {
		return info, err
	}
	info.ID = strings.TrimPrefix(raw, "https://www.youtube.com/watch?v=")
	return info, validatePlatformInfo(info)
}

func TestCorePerformanceMixedPipelineObservation(t *testing.T) {
	work := os.Getenv("CUTMY_CORE_PERF_MIXED_WORK")
	if work == "" {
		t.Skip("opt-in supplemental mixed pipeline witness")
	}
	result := os.Getenv("CUTMY_CORE_PERF_MIXED_RESULT")
	if result == "" || os.Getenv("CUTMY_CORE_PERF_IDLE_WINDOW") == "" {
		t.Fatal("owned result and idle window required")
	}
	preparation, err := strconv.Atoi(os.Getenv("CUTMY_CORE_PERF_MIXED_PREPARATION"))
	if err != nil || preparation < 0 || preparation > 1 {
		t.Fatal("fixed P0/P1 variant required")
	}
	metadata, err := os.ReadFile(os.Getenv("CUTMY_CORE_PERF_MIXED_METADATA"))
	if err != nil {
		t.Fatal(err)
	}
	state := &corePerfMixedState{fixture: os.Getenv("CUTMY_CORE_PERF_MIXED_HLS"), metadata: metadata}
	corePerfMixedFixture = state
	defer func() { corePerfMixedFixture = nil }()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	initial := testStore(t)
	pool := func() *Store {
		config := initial.DB.Config()
		config.MaxConns = 4
		db, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal("independent pool creation failed")
		}
		t.Cleanup(db.Close)
		return &Store{DB: db}
	}
	apiStore, workerStore := pool(), pool()
	c := Config{DataDir: filepath.Join(work, "service"), WorkerHealthPath: filepath.Join(work, "worker-health"), FFmpeg: os.Getenv("FFMPEG_PATH"), FFprobe: os.Getenv("FFPROBE_PATH"), FFmpegProfile: "fast", FFmpegThreads: 2, WorkerConcurrency: 2,
		MaxSourceBytes: 128 << 20, MaxFetchBytes: 128 << 20, MaxOutputBytes: 64 << 20, MaxStorageBytes: 2 << 30, MaxOwnerBytes: 512 << 20, StorageSafetyBytes: 64 << 20, MaxRanges: 32, MaxActiveJobs: 32, MutationsPerMinute: 1000, MaxRangeMS: 600000, MaxJobMS: 3600000,
		SourceTimeout: 20 * time.Second, JobTimeout: 120 * time.Second, UploadTimeout: 20 * time.Second, SourceTTL: time.Hour, ArtifactTTL: time.Hour, StorageWaitTimeout: time.Minute}
	c = corePerfMixedConfig(c, preparation)
	server := httptest.NewServer(NewServer(c, apiStore).Handler())
	defer server.Close()
	request := func(client *http.Client, method, path string, body io.Reader, contentType string) ([]byte, int, error) {
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
		if err != nil {
			return nil, 0, err
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		response, err := client.Do(r)
		if err != nil {
			return nil, 0, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		return data, response.StatusCode, err
	}
	clients := make([]*http.Client, 4)
	sources := make([]Source, 4)
	jobs := make([]Job, 4)
	accepted := make([]int64, 4)
	input, err := os.ReadFile(os.Getenv("CUTMY_CORE_PERF_MIXED_LOCAL"))
	if err != nil {
		t.Fatal(err)
	}
	var localInputs []string
	for owner := range clients {
		jar, _ := cookiejar.New(nil)
		clients[owner] = &http.Client{Jar: jar, Timeout: 25 * time.Second}
		if _, status, err := request(clients[owner], http.MethodGet, "/api/v1/session", nil, ""); err != nil || status != 200 {
			t.Fatal("session", status, err)
		}
		var data []byte
		var status int
		if owner < 2 {
			body, _ := json.Marshal(map[string]string{"url": fmt.Sprintf("https://www.youtube.com/watch?v=SlowNet000%d", owner+1)})
			data, status, err = request(clients[owner], http.MethodPost, "/api/v1/sources", bytes.NewReader(body), "application/json")
		} else {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			part, e := form.CreateFormFile("file", "controlled-1080p60.mp4")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = part.Write(input); e != nil {
				t.Fatal(e)
			}
			if e = form.Close(); e != nil {
				t.Fatal(e)
			}
			data, status, err = request(clients[owner], http.MethodPost, "/api/v1/uploads", &body, form.FormDataContentType())
		}
		if err != nil || status != 201 {
			t.Fatal("source admission", owner, status, err, string(data))
		}
		if err = json.Unmarshal(data, &sources[owner]); err != nil || sources[owner].ID == "" {
			t.Fatal("source response", err)
		}
		if owner >= 2 {
			var path string
			if err := apiStore.DB.QueryRow(ctx, "SELECT path FROM sources WHERE id=$1", sources[owner].ID).Scan(&path); err != nil {
				t.Fatal(err)
			}
			localInputs = append(localInputs, path)
		}
	}
	// Configure input classification only after real uploads publish their paths.
	for _, tool := range []string{c.FFmpeg, c.FFprobe} {
		data, err := os.ReadFile(tool + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		config["LocalInputs"] = localInputs
		data, _ = json.Marshal(config)
		if err := os.WriteFile(tool+".json", data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for owner := range clients {
		ranges := []Range{{StartMS: 2400, EndMS: 8400, Label: "local-first"}, {StartMS: 14200, EndMS: 20200, Label: "local-second"}}
		if owner < 2 {
			ranges = []Range{{StartMS: 1450, EndMS: 2550, Label: "remote-first"}, {StartMS: 3650, EndMS: 4750, Label: "remote-second"}, {StartMS: 5850, EndMS: 6950, Label: "remote-third"}, {StartMS: 8050, EndMS: 9150, Label: "remote-fourth"}}
		}
		body, _ := json.Marshal(ExportRequest{SourceID: sources[owner].ID, Ranges: ranges, Format: "mp4", Quality: "best", CutMode: "accurate"})
		data, status, err := request(clients[owner], http.MethodPost, "/api/v1/jobs", bytes.NewReader(body), "application/json")
		if err != nil || status != 202 {
			t.Fatal("ordered job admission", owner, status, err, string(data))
		}
		if err := json.Unmarshal(data, &jobs[owner]); err != nil || jobs[owner].ID == "" {
			t.Fatal("job response", err)
		}
		accepted[owner] = time.Now().UnixNano()
	}
	burst := time.Now()
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- RunWorker(workerCtx, c, workerStore) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopWorker()
		select {
		case err := <-done:
			if err != nil {
				t.Error("worker cleanup", err)
			}
		case <-time.After(8 * time.Second):
			t.Error("worker cleanup timed out")
		}
		stopped = true
	}
	defer stop()
	if err := os.MkdirAll(filepath.Join(work, "downloads"), 0700); err != nil {
		t.Fatal(err)
	}
	complete := make([]bool, 4)
	count, maxClaimed := 0, 0
	var outputs []map[string]any
	finished := make([]int64, 4)
	polls := make([]int, 4)
	for count < 4 {
		if ctx.Err() != nil {
			t.Fatal("mixed witness deadline", ctx.Err())
		}
		var running int
		if err := apiStore.DB.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE status='running'").Scan(&running); err != nil {
			t.Fatal(err)
		}
		maxClaimed = max(maxClaimed, running)
		if running > 2+preparation {
			t.Fatal("worker overclaimed", running)
		}
		for owner := range jobs {
			if complete[owner] {
				continue
			}
			data, status, err := request(clients[owner], http.MethodGet, "/api/v1/jobs/"+jobs[owner].ID, nil, "")
			polls[owner]++
			if err != nil || status != 200 {
				var failure struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				_ = json.Unmarshal(data, &failure)
				t.Fatal("poll", status, "code", failure.Error.Code, "error", err)
			}
			var job Job
			if err := json.Unmarshal(data, &job); err != nil {
				t.Fatal(err)
			}
			if job.Status == "failed" || job.Status == "cancelled" {
				t.Fatal("job failed", owner, string(data))
			}
			if job.Status != "succeeded" {
				continue
			}
			for cut, item := range job.Items {
				if item.Artifact == nil || item.Status != "succeeded" {
					t.Fatal("missing artifact")
				}
				r, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+item.Artifact.DownloadURL, nil)
				response, err := clients[owner].Do(r)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(work, "downloads", fmt.Sprintf("owner-%d-cut-%d.mp4", owner, cut))
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					response.Body.Close()
					t.Fatal(err)
				}
				n, copyErr := io.Copy(file, io.LimitReader(response.Body, c.MaxOutputBytes+1))
				response.Body.Close()
				closeErr := file.Close()
				if response.StatusCode != 200 || copyErr != nil || closeErr != nil || n != item.Artifact.SizeBytes {
					t.Fatal("download failed", n, copyErr, closeErr)
				}
				outputs = append(outputs, map[string]any{"path": path, "owner": owner, "cut": cut, "actual_start_ms": item.Artifact.ActualStartMS, "actual_end_ms": item.Artifact.ActualEndMS, "segments": item.Artifact.Segments, "track_manifest": item.Artifact.TrackManifest})
			}
			finished[owner] = time.Now().UnixNano()
			complete[owner] = true
			count++
		}
		if count < 4 {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	end := time.Now()
	stop()
	var reserved int64
	if err := apiStore.DB.QueryRow(ctx, "SELECT reserved_bytes::bigint FROM storage_counters WHERE id=1").Scan(&reserved); err != nil || reserved != 0 {
		t.Fatal("work reservation leaked", reserved, err)
	}
	state.mu.Lock()
	network := append([]corePerfMixedNetwork(nil), state.requests...)
	state.mu.Unlock()
	// Polling completion order is irrelevant; independent comparison uses fixed owner/cut order.
	ordered := make([]map[string]any, 0, len(outputs))
	for owner := 0; owner < 4; owner++ {
		for cut := 0; cut < 4; cut++ {
			for _, output := range outputs {
				if output["owner"] == owner && output["cut"] == cut {
					ordered = append(ordered, output)
				}
			}
		}
	}
	report := map[string]any{"outputs": ordered, "preparation": preparation, "burst_start_ns": burst.UnixNano(), "burst_end_ns": end.UnixNano(), "burst_seconds": end.Sub(burst).Seconds(), "jobs_per_minute": 4 * 60 / end.Sub(burst).Seconds(), "accepted_ns": accepted, "all_downloads_ns": finished, "poll_interval_ms": 500, "poll_requests_per_owner": polls, "max_observed_claimed": maxClaimed, "reserved_bytes_at_stop": reserved, "network": network, "scope": "Supplemental in-process HTTP API and worker with independent Stores/pools; fixed HTTPS fixture transport and extractor JSON adapter, not public network or process-isolated production throughput. Burst completion includes up to 500ms client polling quantization; native tool/network intervals distinguish actual work."}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(result, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
