package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

type sdkAgentSidecar struct {
	ClipID   string `json:"clip_id"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	CueCount int    `json:"cue_count"`
}

func agentBrowserRequest(t *testing.T, client *http.Client, method, target string, body any, result any) int {
	t.Helper()
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, target, input)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal("project browser transport failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		t.Fatal(err)
	}
	if result != nil && response.StatusCode < 400 {
		if err = json.Unmarshal(data, result); err != nil {
			t.Fatal("invalid public project response")
		}
	}
	return response.StatusCode
}

// This supplements the existing crash/ranged-download collection test. It
// exercises installed SDKs against real source receipts, worker failure/retry,
// a separately owned scoped reviewer and subsequent project adoption.
func TestInstalledSDKAgentProjectWorkflow(t *testing.T) {
	for _, consumer := range sdkCollectionConsumers(t, true) {
		t.Run(consumer.name, func(t *testing.T) {
			store := testStore(t)
			config := mediaConfig(t)
			config.WorkerConcurrency, config.FFmpegThreads = 1, 1
			config.SourceTimeout, config.UploadTimeout, config.JobTimeout = 2*time.Minute, 2*time.Minute, 2*time.Minute
			config.SourceTTL, config.ArtifactTTL, config.StorageWaitTimeout = time.Hour, time.Hour, time.Minute
			config.MaxStorageBytes, config.MaxOwnerBytes, config.MaxActiveJobs, config.MutationsPerMinute = 1<<30, 1<<30, 32, 200
			config.MaxSourceBytes, config.MaxOutputBytes, config.MaxRanges = 128<<20, 64<<20, 128
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			real := os.Getenv("CUTMY_SDK_COLLECTION_INTERVIEW") != ""
			input := os.Getenv("CUTMY_SDK_COLLECTION_INTERVIEW")
			width, height := 160, 96
			if !real {
				input = sdkMediaFixture(t, ctx, config)
			} else {
				width, height = 1280, 720
				raw, err := os.ReadFile(input)
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(raw)
				if !filepath.IsAbs(input) || len(raw) != 82242740 || hex.EncodeToString(hash[:]) != "0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca" {
					t.Fatal("real interview identity differs")
				}
			}
			work := sdkCollectionPrivateDir(t)
			root, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			ext := ".py"
			if consumer.name == "typescript" {
				ext = ".mjs"
			}
			consumer.script = filepath.Join(root, "scripts", "sdk-agent-project-acceptance"+ext)
			application := NewServer(config, store).Handler()
			var mu sync.Mutex
			lostSource, lostRetry := false, false
			webAssets := os.Getenv("CUTMY_AGENT_PROJECT_WEB_ASSETS")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if webAssets != "" && !strings.HasPrefix(r.URL.Path, "/api/") {
					if r.URL.Path == "/" {
						http.ServeFile(w, r, filepath.Join(webAssets, "index.html"))
					} else {
						http.FileServer(http.Dir(webAssets)).ServeHTTP(w, r)
					}
					return
				}
				key := r.Header.Get("Idempotency-Key")
				if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/uploads" || r.URL.Path == "/api/v1/collections" && strings.HasPrefix(key, "retry-")) {
					rr := httptest.NewRecorder()
					application.ServeHTTP(rr, r)
					mu.Lock()
					drop := false
					if r.URL.Path == "/api/v1/uploads" && rr.Code == 201 && !lostSource {
						lostSource = true
						drop = true
					}
					if r.URL.Path == "/api/v1/collections" && rr.Code == 202 && !lostRetry {
						lostRetry = true
						drop = true
					}
					mu.Unlock()
					if drop {
						conn, _, e := w.(http.Hijacker).Hijack()
						if e == nil {
							_ = conn.Close()
							return
						}
					}
					for name, values := range rr.Header() {
						w.Header()[name] = values
					}
					w.WriteHeader(rr.Code)
					_, _ = w.Write(rr.Body.Bytes())
					return
				}
				application.ServeHTTP(w, r)
			}))
			defer server.Close()
			run := func(action string, kill bool) sdkCollectionReport {
				return runSDKCollectionConsumer(t, ctx, consumer, action, input, work, server.URL, kill)
			}
			run("prepare-kill", true)
			prepared := run("prepare-resume", false)
			var sourceCount int
			if err = store.DB.QueryRow(ctx, "SELECT count(*) FROM sources").Scan(&sourceCount); err != nil || sourceCount != 1 || prepared.SourceID == "" {
				t.Fatal("source uncertainty duplicated/lost its committed source")
			}
			var uploadedPath string
			if err = store.DB.QueryRow(ctx, "SELECT path FROM sources WHERE id=$1", prepared.SourceID).Scan(&uploadedPath); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			uploaded, err := os.ReadFile(uploadedPath)
			if err != nil || !bytes.Equal(original, uploaded) {
				t.Fatal("resumed preparation changed or truncated the source")
			}
			plan := sdkCollectionPlan(real)
			request := CollectionRequest{SourceID: prepared.SourceID, Plan: plan, Rendition: "final"}
			sdkCollectionWriteJSON(t, filepath.Join(work, "request.json"), request)
			var sourceOwner string
			if err = store.DB.QueryRow(ctx, "SELECT owner FROM sources WHERE id=$1", prepared.SourceID).Scan(&sourceOwner); err != nil {
				t.Fatal(err)
			}
			preparedSource, err := store.Source(ctx, prepared.SourceID, sourceOwner)
			if err != nil {
				t.Fatal(err)
			}
			initialClips, err := NormalizeCollection(plan, CollectionExecution{Rendition: "final"}, config, preparedSource)
			if err != nil || len(initialClips) != 15 {
				t.Fatal("invalid initial failure fixture")
			}
			failedAttemptKey := collectionJobKey("agent-project-acceptance", initialClips[14])
			// Use the production claim and job processor. Only the first attempt of
			// clip-15 receives an already-expired execution budget; all other jobs
			// use ordinary tools, publication, storage leases and processing logic.
			for _, dir := range []string{"work", "artifacts"} {
				if err = fsdurable.EnsureDirectory(filepath.Join(config.DataDir, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			workerCtx, stop := context.WithCancel(withWorkerMediaBudget(ctx, config))
			done := make(chan error, 1)
			workerFailure := func(stage string, failure error) {
				state := "none"
				var postgres interface{ SQLState() string }
				if errors.As(failure, &postgres) {
					candidate := postgres.SQLState()
					if len(candidate) == 5 && strings.IndexFunc(candidate, func(r rune) bool { return !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", r) }) == -1 {
						state = candidate
					}
				}
				// Never format the raw connection/query error: it may contain a
				// DSN. A stopped fixture worker must stop its waiting SDK immediately.
				t.Logf("agent_acceptance_worker_stopped stage=%s error_class=%T context_deadline=%t context_cancelled=%t sqlstate=%s", stage, failure, errors.Is(failure, context.DeadlineExceeded), errors.Is(failure, context.Canceled), state)
				done <- failure
				cancel()
			}
			go func() {
				for {
					if workerCtx.Err() != nil {
						done <- nil
						return
					}
					job, token, e := claimWorkerJob(workerCtx, store)
					if errors.Is(e, ErrNotFound) {
						select {
						case <-workerCtx.Done():
							done <- nil
							return
						case <-time.After(20 * time.Millisecond):
						}
						continue
					}
					if e != nil {
						workerFailure("claim", e)
						return
					}
					var actualKey string
					if e = store.DB.QueryRow(workerCtx, "SELECT idempotency_key FROM jobs WHERE id=$1", job.ID).Scan(&actualKey); e != nil {
						workerFailure("claimed_job_identity", e)
						return
					}
					c := config
					if actualKey == failedAttemptKey {
						c.JobTimeout = time.Nanosecond
					}
					processJob(workerCtx, c, store, job, token)
				}
			}()
			t.Cleanup(func() {
				stop()
				select {
				case e := <-done:
					if e != nil {
						t.Error("agent acceptance worker failed")
					}
				case <-time.After(10 * time.Second):
					t.Error("agent acceptance worker did not stop")
				}
			})
			failed := run("run-fail", false)
			if failed.Complete || len(failed.Outputs) != 14 {
				t.Fatal("worker failure was not isolated to one clip")
			}
			prior := map[string]sdkCollectionOutput{}
			for _, out := range failed.Outputs {
				sdkCollectionMatchesStoredArtifact(t, ctx, store, out)
				prior[out.ClipID] = out
			}
			run("repair-kill", true)
			repaired := run("repair-resume", false)
			if !repaired.Complete || len(repaired.Outputs) != 15 {
				t.Fatal("explicit repair did not complete fifteen clips")
			}
			for _, out := range repaired.Outputs {
				if old, ok := prior[out.ClipID]; ok && (old.JobID != out.JobID || old.SHA256 != out.SHA256 || old.Path != out.Path) {
					t.Fatal("repair replaced an unaffected output")
				}
			}
			checkCounts := func(jobs, artifacts int) {
				t.Helper()
				var j, a, attempts int
				if e := store.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM jobs),(SELECT count(*) FROM artifacts),(SELECT COALESCE(sum(attempts),0) FROM jobs)").Scan(&j, &a, &attempts); e != nil || j != jobs || a != artifacts || attempts != jobs {
					t.Fatalf("unexpected work counts jobs=%d artifacts=%d attempts=%d; expected %d/%d", j, a, attempts, jobs, artifacts)
				}
			}
			checkCounts(16, 15)
			project := run("make-project", false)
			if project.ProjectID == "" {
				t.Fatal("no project for browser review")
			}
			var control struct {
				BaseURL   string `json:"base_url"`
				ProjectID string `json:"project_id"`
				Token     string `json:"token"`
			}
			raw, err := os.ReadFile(filepath.Join(work, "browser-private.json"))
			if err != nil || json.Unmarshal(raw, &control) != nil {
				t.Fatal("missing private review control")
			}
			revised := sdkCollectionPlan(real)
			revised.Clips[0].Ranges[0].StartMS += 40
			revised.Clips[14].Options.Crop = &Crop{X: .25, Y: 0, Width: .5, Height: 1}
			if driver := os.Getenv("CUTMY_AGENT_PROJECT_BROWSER_DRIVER"); driver != "" {
				if !filepath.IsAbs(driver) || webAssets == "" {
					t.Fatal("browser driver requires absolute paths and built assets")
				}
				cmd := exec.CommandContext(ctx, consumerNode(), driver, filepath.Join(work, "browser-private.json"), filepath.Join(work, "browser-review.json"))
				log, err := os.OpenFile(filepath.Join(work, "browser-private.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stdout, cmd.Stderr = log, log
				e := cmd.Run()
				_ = log.Close()
				if e != nil {
					t.Fatal("real browser review failed; diagnostics remain private")
				}
				var receipt struct {
					Complete bool `json:"complete"`
				}
				raw, e := os.ReadFile(filepath.Join(work, "browser-review.json"))
				if e != nil || json.Unmarshal(raw, &receipt) != nil || !receipt.Complete {
					t.Fatal("browser review incomplete")
				}
			} else {
				jar, _ := cookiejar.New(nil)
				browser := &http.Client{Jar: jar, Timeout: 30 * time.Second}
				if status := agentBrowserRequest(t, browser, "GET", server.URL+"/api/v1/session", nil, &map[string]any{}); status != 200 {
					t.Fatal("browser session unavailable")
				}
				if status := agentBrowserRequest(t, browser, "GET", server.URL+"/api/v1/sources/"+prepared.SourceID, nil, nil); status != 404 {
					t.Fatal("reviewer gained unscoped owner access")
				}
				var redeemed struct {
					Project AgentProject `json:"project"`
				}
				if status := agentBrowserRequest(t, browser, "POST", server.URL+"/api/v1/projects/handoffs/redeem", map[string]string{"token": control.Token}, &redeemed); status != 200 {
					t.Fatal("project handoff redemption failed")
				}
				edit := projectEditRequest{Revision: redeemed.Project.Revision, Plan: revised, Rendition: "final"}
				var updated AgentProject
				if status := agentBrowserRequest(t, browser, "PUT", server.URL+"/api/v1/projects/"+project.ProjectID, edit, &updated); status != 200 {
					t.Fatal("scoped two-clip edit failed")
				}
				if status := agentBrowserRequest(t, browser, "POST", server.URL+"/api/v1/projects/"+project.ProjectID+"/collections", map[string]any{}, &CollectionSubmission{}); status != 202 {
					t.Fatal("scoped edited submission failed")
				}
				var replay struct {
					Project AgentProject `json:"project"`
				}
				if status := agentBrowserRequest(t, browser, "POST", server.URL+"/api/v1/projects/handoffs/redeem", map[string]string{"token": control.Token}, &replay); status != 200 || replay.Project.ID != project.ProjectID {
					t.Fatal("same reviewer could not recover a lost redemption reply")
				}
				otherJar, _ := cookiejar.New(nil)
				other := &http.Client{Jar: otherJar, Timeout: 30 * time.Second}
				if status := agentBrowserRequest(t, other, "GET", server.URL+"/api/v1/session", nil, &map[string]any{}); status != 200 {
					t.Fatal("second reviewer session unavailable")
				}
				if status := agentBrowserRequest(t, other, "POST", server.URL+"/api/v1/projects/handoffs/redeem", map[string]string{"token": control.Token}, nil); status != 404 {
					t.Fatal("spent handoff admitted a different reviewer")
				}
			}
			synced := run("sync-project", false)
			if !synced.Complete || len(synced.Outputs) != 15 {
				t.Fatal("SDK did not adopt the complete reviewed project")
			}
			before := map[string]sdkCollectionOutput{}
			for _, out := range repaired.Outputs {
				before[out.ClipID] = out
			}
			for i, out := range synced.Outputs {
				if i != 0 && i != 14 {
					old := before[out.ClipID]
					if old.JobID != out.JobID || old.SHA256 != out.SHA256 || old.Path != out.Path {
						t.Fatal("browser edits replaced an unaffected clip")
					}
				}
				w := width
				if i == 2 || i == 14 {
					w = width / 2
				}
				checkSDKCollectionMedia(t, ctx, config, out, revised.Clips[i].Ranges, w, height)
				sdkCollectionMatchesStoredArtifact(t, ctx, store, out)
			}
			checkCounts(18, 17)
			replayed := run("replay", false)
			if !replayed.Complete || len(replayed.Outputs) != 15 {
				t.Fatal("reviewed collection replay failed")
			}
			checkCounts(18, 17)
			if err = os.WriteFile(filepath.Join(work, "supplied.srt"), []byte("1\n00:00:00,000 --> 00:10:00,000\nПроверка заданных таймкодов\n"), 0600); err != nil {
				t.Fatal(err)
			}
			captions := run("subtitles", false)
			if len(captions.Sidecars) != 15 {
				t.Fatal("not every clip received a subtitle sidecar")
			}
			for i, sidecar := range captions.Sidecars {
				data, e := os.ReadFile(sidecar.Path)
				if e != nil {
					t.Fatal(e)
				}
				sum := sha256.Sum256(data)
				cues, e := parseSRT(data)
				if e != nil || sidecar.SHA256 != hex.EncodeToString(sum[:]) || len(cues) != len(synced.Outputs[i].Segments) || sidecar.CueCount != len(cues) {
					t.Fatal("invalid sidecar bytes/receipts")
				}
				for j, cue := range cues {
					segment := synced.Outputs[i].Segments[j]
					if cue.StartMS != segment.OutputStartMS || cue.EndMS > segment.OutputEndMS || cue.EndMS < segment.OutputEndMS-100 || cue.Text != "Проверка заданных таймкодов" {
						t.Fatal("subtitle cue did not follow actual cut/order")
					}
				}
			}
			checkCounts(18, 17)
			mu.Lock()
			bothDropped := lostSource && lostRetry
			mu.Unlock()
			if !bothDropped {
				t.Fatal("lost committed replies were not actually exercised")
			}
			if evidence := os.Getenv("CUTMY_AGENT_PROJECT_EVIDENCE"); filepath.IsAbs(evidence) {
				dir := filepath.Join(evidence, consumer.name)
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for i, out := range synced.Outputs {
					raw, e := os.ReadFile(out.Path)
					if e != nil {
						t.Fatal(e)
					}
					path := filepath.Join(dir, out.ClipID+".mp4")
					if e = os.WriteFile(path, raw, 0600); e != nil {
						t.Fatal(e)
					}
					synced.Outputs[i].Path = path
				}
				for i, out := range captions.Sidecars {
					raw, e := os.ReadFile(out.Path)
					if e != nil {
						t.Fatal(e)
					}
					path := filepath.Join(dir, out.ClipID+".srt")
					if e = os.WriteFile(path, raw, 0600); e != nil {
						t.Fatal(e)
					}
					captions.Sidecars[i].Path = path
				}
				if os.Getenv("CUTMY_AGENT_PROJECT_BROWSER_DRIVER") != "" {
					raw, e := os.ReadFile(filepath.Join(work, "browser-review.json"))
					if e != nil {
						t.Fatal(e)
					}
					var review map[string]any
					if json.Unmarshal(raw, &review) != nil {
						t.Fatal("invalid public browser receipt")
					}
					review["screenshot"] = filepath.Join(dir, "browser-review-desktop.png")
					sdkCollectionWriteJSON(t, filepath.Join(dir, "browser-review.json"), review)
					raw, e = os.ReadFile(filepath.Join(work, "browser-review-desktop.png"))
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(dir, "browser-review-desktop.png"), raw, 0600); e != nil {
						t.Fatal(e)
					}
				}
				sdkCollectionWriteJSON(t, filepath.Join(dir, "requests.json"), map[string]any{"initial": request, "reviewed": CollectionRequest{SourceID: prepared.SourceID, Plan: revised, Rendition: "final"}})
				sdkCollectionWriteJSON(t, filepath.Join(dir, "acceptance.json"), map[string]any{"complete": true, "source_count": 1, "clips": 15, "jobs": 18, "artifacts": 17, "lost_source_reply": true, "lost_retry_reply": true, "browser_review": os.Getenv("CUTMY_AGENT_PROJECT_BROWSER_DRIVER") != "", "outputs": synced.Outputs, "sidecars": captions.Sidecars})
			}
			t.Log("installed SDK completed durable source, fifteen clips, isolated repair, scoped review, unchanged replay and fifteen verified sidecars")
		})
	}
}

func consumerNode() string {
	if node := os.Getenv("CUTMY_SDK_NODE"); node != "" {
		return node
	}
	return "node"
}
