package app

// Identical HTTP/worker witness for both runtime revisions. The separately
// recorded config adapter enables additional preparation only in labelled runs.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type corePerfServerScenario struct {
	ID, Profile, Fixture string
	Owners               int     `json:"owners"`
	JobsPerOwner         int     `json:"jobs_per_owner"`
	CutsPerJob           int     `json:"cuts_per_job"`
	WorkerConcurrency    int     `json:"worker_concurrency"`
	FFmpegThreads        int     `json:"ffmpeg_threads"`
	Ranges               []Range `json:"ranges"`
}
type corePerfServerJob struct {
	ID                   string
	Attempts             []map[string]any
	Accepted, Downloaded time.Time
	Outputs              []map[string]any
	Refused, Complete    bool
}
type corePerfServerLog struct {
	Job             string
	At              time.Time
	ElapsedMS       int64
	Event           string
	Fields          map[string]any
	TimestampSource string `json:",omitempty"`
}
type corePerfServerHandler struct {
	next    slog.Handler
	mu      *sync.Mutex
	records *[]corePerfServerLog
}

func (h *corePerfServerHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h *corePerfServerHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "job_processing_stopped" || record.Message == "job_claimed" || record.Message == "source_inspected" || record.Message == "source_streams_resolved" || record.Message == "source_preparation_finished" || record.Message == "media_processing_wait" || record.Message == "media_processing_finished" || record.Message == "artifact_publication_finished" || record.Message == "media_subprocess_finished" {
		entry := corePerfServerLog{At: record.Time, Event: record.Message, Fields: make(map[string]any)}
		record.Attrs(func(attribute slog.Attr) bool {
			if attribute.Key == "job_id" {
				entry.Job = attribute.Value.String()
			}
			if attribute.Key == "elapsed_ms" {
				entry.ElapsedMS = attribute.Value.Int64()
			}
			switch attribute.Key {
			case "elapsed_ms", "claim_elapsed_ms", "network_bytes", "size_bytes", "cache_hit", "success", "reused", "scope", "tool", "item_id":
				entry.Fields[attribute.Key] = attribute.Value.Any()
			}
			return true
		})
		h.mu.Lock()
		*h.records = append(*h.records, entry)
		h.mu.Unlock()
	}
	return h.next.Handle(ctx, record)
}
func (h *corePerfServerHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	return &corePerfServerHandler{next: h.next.WithAttrs(attributes), mu: h.mu, records: h.records}
}
func (h *corePerfServerHandler) WithGroup(name string) slog.Handler {
	return &corePerfServerHandler{next: h.next.WithGroup(name), mu: h.mu, records: h.records}
}

func TestCorePerformanceServerObservation(t *testing.T) {
	scenarioPath := os.Getenv("CUTMY_CORE_PERF_SERVER_SCENARIO")
	if scenarioPath == "" {
		t.Skip("opt-in frozen server observation")
	}
	fixture, work, resultPath := os.Getenv("CUTMY_CORE_PERF_SERVER_FIXTURE"), os.Getenv("CUTMY_CORE_PERF_SERVER_WORK"), os.Getenv("CUTMY_CORE_PERF_SERVER_RESULT")
	if fixture == "" || work == "" || resultPath == "" || os.Getenv("CUTMY_CORE_PERF_IDLE_WINDOW") == "" {
		t.Fatal("owned fixture/work/result and coordinator window are required")
	}
	var scenario corePerfServerScenario
	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"scenario": scenario.ID, "complete": false}
	defer func() {
		report["complete"] = !t.Failed()
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error("server report encoding failed", err)
			return
		}
		file, err := os.OpenFile(resultPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Error("exclusive server report publication failed", err)
			return
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	if (scenario.Owners != 1 && scenario.Owners != 4) || scenario.JobsPerOwner != 4 || scenario.CutsPerJob != 2 || len(scenario.Ranges) != 2 || scenario.WorkerConcurrency != 2 || scenario.FFmpegThreads != 2 {
		t.Fatal("server scenario differs from preregistered workload")
	}
	input, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(input)
	if hex.EncodeToString(digest[:]) != os.Getenv("CUTMY_CORE_PERF_SERVER_FIXTURE_SHA256") {
		t.Fatal("frozen server fixture bytes changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()
	initial := testStore(t)
	openIndependent := func() *Store {
		config := initial.DB.Config()
		config.MaxConns = 4
		db, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal("independent owned pool creation failed")
		}
		t.Cleanup(db.Close)
		return &Store{DB: db}
	}
	apiStore, workerStore := openIndependent(), openIndependent()
	c := Config{DataDir: filepath.Join(work, "service"), WorkerHealthPath: filepath.Join(work, "worker-health"), FFmpeg: os.Getenv("FFMPEG_PATH"), FFprobe: os.Getenv("FFPROBE_PATH"), FFmpegProfile: "fast", FFmpegThreads: 2, WorkerConcurrency: 2,
		MaxSourceBytes: 128 << 20, MaxFetchBytes: 128 << 20, MaxOutputBytes: 32 << 20, MaxStorageBytes: 2 << 30, MaxOwnerBytes: 512 << 20, StorageSafetyBytes: 64 << 20, MaxRanges: 32, MaxActiveJobs: 32, MutationsPerMinute: 1000, MaxRangeMS: 600000, MaxJobMS: 3600000,
		SourceTimeout: 30 * time.Second, JobTimeout: 150 * time.Second, UploadTimeout: 30 * time.Second, SourceTTL: time.Hour, ArtifactTTL: time.Hour, StorageWaitTimeout: time.Minute}
	c = corePerformanceServerConfig(c)
	if c.FFmpeg == "" || c.FFprobe == "" || int64(len(input)) > c.MaxSourceBytes {
		t.Fatal("frozen tools/input exceed registered server settings")
	}
	if err := os.MkdirAll(filepath.Join(work, "downloads"), 0700); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(filepath.Dir(resultPath), "worker-stages.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	var logMu sync.Mutex
	var logs []corePerfServerLog
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(&corePerfServerHandler{next: slog.NewJSONHandler(logFile, nil), mu: &logMu, records: &logs}))
	defer slog.SetDefault(previousLogger)
	var serverURL string
	var stop func()
	var roleResults func() map[string]any
	if os.Getenv("CUTMY_CORE_PERF_SERVER_CLI") != "" {
		serverURL, stop, roleResults = corePerformanceStartRoles(t, ctx, c, initial.DB.Config().ConnConfig.ConnString(), filepath.Dir(resultPath))
	} else {
		server := httptest.NewServer(NewServer(c, apiStore).Handler())
		serverURL = server.URL
		workerCtx, stopWorker := context.WithCancel(ctx)
		workerDone := make(chan error, 1)
		go func() { workerDone <- RunWorker(workerCtx, c, workerStore) }()
		stopped := false
		stop = func() {
			if stopped {
				return
			}
			stopWorker()
			select {
			case err := <-workerDone:
				if err != nil {
					t.Error("owned worker stopped with error", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("owned worker cleanup did not finish")
			}
			server.Close()
			stopped = true
		}
		roleResults = func() map[string]any {
			return map[string]any{"mode": "supplemental_in_process", "resource_scope": "Whole helper process tree; no per-role resource separation"}
		}
	}
	defer func() { stop(); report["roles"] = roleResults() }()
	clients := make([]*http.Client, scenario.Owners)
	sources := make([]Source, scenario.Owners)
	var uploads []map[string]any
	request := func(client *http.Client, method, path string, body io.Reader, contentType, key string) ([]byte, int, error) {
		r, err := http.NewRequestWithContext(ctx, method, serverURL+path, body)
		if err != nil {
			return nil, 0, err
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(r)
		if err != nil {
			return nil, 0, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		return data, response.StatusCode, err
	}
	for owner := range clients {
		jar, _ := cookiejar.New(nil)
		clients[owner] = &http.Client{Jar: jar, Timeout: 30 * time.Second}
		if _, status, err := request(clients[owner], http.MethodGet, "/api/v1/session", nil, "", ""); err != nil || status != http.StatusOK {
			t.Fatal("owner session creation failed", status, err)
		}
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "frozen-complex.mp4")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(input); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		data, status, err := request(clients[owner], http.MethodPost, "/api/v1/uploads", &body, form.FormDataContentType(), "")
		if err != nil || status != http.StatusCreated {
			t.Fatal("frozen HTTP upload failed", status, err)
		}
		if err := json.Unmarshal(data, &sources[owner]); err != nil || sources[owner].ID == "" {
			t.Fatal("upload returned no source", err)
		}
		uploads = append(uploads, map[string]any{"owner": owner, "seconds": time.Since(start).Seconds(), "source_bytes": len(input)})
	}
	jobs := make([][]corePerfServerJob, scenario.Owners)
	for owner := range jobs {
		jobs[owner] = make([]corePerfServerJob, scenario.JobsPerOwner)
	}
	report["jobs_partial"] = jobs
	burst := time.Now()
	var jobsMu sync.Mutex
	submit := func(owner, index int) error {
		options := ExportRequest{SourceID: sources[owner].ID, Ranges: scenario.Ranges, Format: "mp4", Quality: "best", CutMode: "accurate"}
		if scenario.Profile == "copy-mp4" {
			options.CutMode = "copy"
		} else if scenario.Profile == "mp3" {
			options.Format = "mp3"
		} else if scenario.Profile != "accurate-mp4" {
			return errors.New("unknown registered server profile")
		}
		payload, _ := json.Marshal(options)
		started := time.Now()
		data, status, err := request(clients[owner], http.MethodPost, "/api/v1/jobs", bytes.NewReader(payload), "application/json", fmt.Sprintf("frozen-owner-%d-job-%d", owner, index))
		acknowledged := time.Now()
		attempt := map[string]any{"submitted_seconds": started.Sub(burst).Seconds(), "response_seconds": acknowledged.Sub(burst).Seconds(), "status": status}
		jobsMu.Lock()
		defer jobsMu.Unlock()
		job := &jobs[owner][index]
		job.Attempts = append(job.Attempts, attempt)
		if err != nil {
			return err
		}
		if status == http.StatusTooManyRequests {
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &failure)
			attempt["refusal_code"] = failure.Error.Code
			if failure.Error.Code != "job_limit" {
				return errors.New("unexpected server refusal")
			}
			job.Refused = true
			return nil
		}
		if status != http.StatusAccepted {
			return fmt.Errorf("unexpected job HTTP status %d", status)
		}
		var accepted Job
		if err := json.Unmarshal(data, &accepted); err != nil || accepted.ID == "" {
			return errors.New("invalid accepted job")
		}
		if job.ID != "" && job.ID != accepted.ID {
			return errors.New("idempotent resubmission changed job")
		}
		job.ID, job.Accepted, job.Refused = accepted.ID, acknowledged, false
		return nil
	}
	barrier := make(chan struct{})
	failure := make(chan error, scenario.Owners*scenario.JobsPerOwner)
	var admissions sync.WaitGroup
	for owner := range jobs {
		for index := range jobs[owner] {
			admissions.Add(1)
			go func() {
				defer admissions.Done()
				<-barrier
				if err := submit(owner, index); err != nil {
					failure <- err
				}
			}()
		}
	}
	close(barrier)
	admissions.Wait()
	close(failure)
	for err := range failure {
		t.Fatal("initial HTTP admission failed", err)
	}
	var outputs []map[string]any
	completed := 0
	for completed < scenario.Owners*scenario.JobsPerOwner {
		if ctx.Err() != nil {
			t.Fatal("server burst exceeded its deadline")
		}
		for owner := range jobs {
			data, status, err := request(clients[owner], http.MethodGet, "/api/v1/jobs", nil, "", "")
			if err != nil || status != http.StatusOK {
				t.Fatal("HTTP history poll failed", status, err)
			}
			var history struct {
				Jobs []jobSummary `json:"jobs"`
			}
			if err := json.Unmarshal(data, &history); err != nil {
				t.Fatal(err)
			}
			states := make(map[string]string)
			for _, job := range history.Jobs {
				states[job.ID] = job.Status
			}
			firstComplete := false
			for index := range jobs[owner] {
				job := &jobs[owner][index]
				if job.Complete {
					firstComplete = true
					continue
				}
				if job.ID == "" {
					continue
				}
				if state := states[job.ID]; state == "failed" || state == "cancelled" {
					t.Fatal("registered export failed", owner, index, state)
				} else if state != "succeeded" {
					continue
				}
				data, status, err := request(clients[owner], http.MethodGet, "/api/v1/jobs/"+job.ID, nil, "", "")
				if err != nil || status != http.StatusOK {
					t.Fatal("terminal HTTP job read failed", status, err)
				}
				var finished Job
				if err := json.Unmarshal(data, &finished); err != nil || len(finished.Items) != 2 {
					t.Fatal("terminal job lost its ranges", err)
				}
				for cut, item := range finished.Items {
					if item.Status != "succeeded" || item.Artifact == nil || item.Artifact.DownloadURL == "" {
						t.Fatal("terminal job lost its artifact")
					}
					r, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+item.Artifact.DownloadURL, nil)
					if err != nil {
						t.Fatal(err)
					}
					response, err := clients[owner].Do(r)
					if err != nil {
						t.Fatal("owned artifact download failed", err)
					}
					if response.StatusCode != http.StatusOK {
						response.Body.Close()
						t.Fatal("owned artifact download was refused", response.StatusCode)
					}
					path := filepath.Join(work, "downloads", fmt.Sprintf("owner-%d-job-%d-cut-%d.%s", owner, index, cut, finished.Request.Format))
					file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						response.Body.Close()
						t.Fatal(err)
					}
					count, copyErr := io.Copy(file, io.LimitReader(response.Body, c.MaxOutputBytes+1))
					response.Body.Close()
					closeErr := file.Close()
					if copyErr != nil || closeErr != nil || count != item.Artifact.SizeBytes || count > c.MaxOutputBytes {
						t.Fatal("artifact receipt was incomplete", count, copyErr, closeErr)
					}
					output := map[string]any{"path": path, "owner": owner, "job": index, "cut": cut, "bytes": count, "label": item.Label, "actual_start_ms": item.Artifact.ActualStartMS, "actual_end_ms": item.Artifact.ActualEndMS}
					if item.Artifact.TrackManifest != nil {
						output["track_manifest"] = item.Artifact.TrackManifest
					}
					if item.Artifact.Segments != nil {
						output["segments"] = item.Artifact.Segments
					}
					job.Outputs = append(job.Outputs, output)
				}
				job.Complete, job.Downloaded = true, time.Now()
				completed++
				firstComplete = true
			}
			// The real owner cap is three active jobs. Preserve every initial
			// refusal, then retry once only after an observed owner completion.
			if firstComplete {
				for index := range jobs[owner] {
					if jobs[owner][index].Refused {
						if err := submit(owner, index); err != nil {
							t.Fatal("post-completion retry failed", err)
						}
						if jobs[owner][index].Refused {
							t.Fatal("owner capacity remained full after completion")
						}
					}
				}
			}
		}
		if completed < scenario.Owners*scenario.JobsPerOwner {
			select {
			case <-ctx.Done():
				t.Fatal("server burst exceeded its deadline")
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	burstElapsed := time.Since(burst)
	stop()
	var jobReports []map[string]any
	var attempted, refused int
	logMu.Lock()
	stageLogs := append([]corePerfServerLog(nil), logs...)
	logMu.Unlock()
	if os.Getenv("CUTMY_CORE_PERF_SERVER_CLI") != "" {
		stageLogs = append(stageLogs, corePerformanceRoleLogs(t, filepath.Join(filepath.Dir(resultPath), "worker-cli.log"))...)
		if len(stageLogs) == 0 {
			t.Fatal("independent worker CLI emitted no observable processing events")
		}
	}
	for owner := range jobs {
		for index, job := range jobs[owner] {
			attempted += len(job.Attempts)
			for _, attempt := range job.Attempts {
				if attempt["status"] == http.StatusTooManyRequests {
					refused++
				}
			}
			report := map[string]any{"owner": owner, "job": index, "attempts": job.Attempts, "accepted_seconds": job.Accepted.Sub(burst).Seconds(), "all_downloads_seconds": job.Downloaded.Sub(burst).Seconds(), "accepted_to_all_downloads_seconds": job.Downloaded.Sub(job.Accepted).Seconds()}
			var created time.Time
			if err := apiStore.DB.QueryRow(ctx, `SELECT created_at FROM jobs WHERE id=$1`, job.ID).Scan(&created); err != nil {
				t.Fatal("post-phase job timestamp read failed", err)
			}
			for _, event := range stageLogs {
				if event.Job == job.ID && event.Event == "job_processing_stopped" {
					start := event.At.Add(-time.Duration(event.ElapsedMS) * time.Millisecond)
					report["queue_wait_estimate_seconds"] = start.Sub(created).Seconds()
					report["processing_elapsed_ms"] = event.ElapsedMS
				}
			}
			for _, event := range stageLogs {
				if event.Job == job.ID && event.Event == "job_claimed" {
					report["queue_wait_seconds"] = event.At.Sub(created).Seconds()
					report["queue_wait_source"] = "claim_ack_log"
					if event.TimestampSource == "complete_log_line_observed" {
						report["queue_wait_source"] = "claim_log_observed_transitclock"
					}
				}
			}
			jobReports = append(jobReports, report)
			outputs = append(outputs, job.Outputs...)
		}
	}
	var stored, reserved int64
	if err := apiStore.DB.QueryRow(ctx, `SELECT stored_bytes::bigint,reserved_bytes::bigint FROM storage_counters WHERE id=1`).Scan(&stored, &reserved); err != nil || reserved != 0 {
		t.Fatal("completed server retained work reservations", reserved, err)
	}
	report = map[string]any{"scenario": scenario.ID, "roles": roleResults(), "outputs": outputs, "uploads": uploads, "jobs": jobReports, "stage_events": stageLogs, "burst_seconds": burstElapsed.Seconds(), "jobs_per_minute": float64(completed) * 60 / burstElapsed.Seconds(), "attempted": attempted, "admitted": completed, "initial_job_limit_refusals": refused, "stored_bytes_at_stop": stored, "reserved_bytes_at_stop": reserved,
		"preparation_variant": os.Getenv("CUTMY_CORE_PERF_SERVER_PREPARATION"), "config": map[string]any{"worker_concurrency": 2, "ffmpeg_threads": 2, "api_pool_max_conns": 4, "worker_pool_max_conns": 4, "max_source_bytes": c.MaxSourceBytes, "max_output_bytes": c.MaxOutputBytes, "max_storage_bytes": c.MaxStorageBytes, "max_owner_bytes": c.MaxOwnerBytes, "storage_safety_bytes": c.StorageSafetyBytes, "max_active_jobs": c.MaxActiveJobs, "mutations_per_minute": c.MutationsPerMinute},
		"scope": "Actual local HTTP sessions/upload/admission/history/status/all artifact bytes; independent API and worker Stores/pools share only owned PostgreSQL/media. Four jobs per owner initially arrive at a barrier, retain real job_limit refusals, retry once after owner completion. Burst clock excludes uploads/setup/post-phase SQL/verification; outer process resources/tool counts include them. Completion polling is 250ms. Queue estimate uses SQL created_at and rounded worker stop elapsed log; clocks and 1ms rounding limit precision. No internet/browser/SLA claim. Per-role process/cgroup scope is recorded separately in roles."}
}
