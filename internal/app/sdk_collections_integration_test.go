package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type sdkCollectionOutput struct {
	ClipID        string            `json:"clip_id"`
	JobID         string            `json:"job_id"`
	ArtifactID    string            `json:"artifact_id"`
	Path          string            `json:"path"`
	SizeBytes     int64             `json:"size_bytes"`
	SHA256        string            `json:"sha256"`
	Rendition     string            `json:"rendition"`
	Segments      []ArtifactSegment `json:"segments"`
	TrackManifest []TrackSelection  `json:"track_manifest"`
}

type sdkCollectionReport struct {
	Complete   bool                  `json:"complete"`
	SourceID   string                `json:"source_id"`
	ProjectID  string                `json:"project_id"`
	Sidecars   []sdkAgentSidecar     `json:"sidecars"`
	Outputs    []sdkCollectionOutput `json:"outputs"`
	Event      string                `json:"event"`
	Checkpoint string                `json:"checkpoint"`
	Report     *sdkCollectionReport  `json:"report"`
	Download   *struct {
		ArtifactID string `json:"artifact_id"`
		Offset     int64  `json:"offset"`
		TotalSize  int64  `json:"total_size"`
	} `json:"download"`
}

func sdkCollectionConsumers(t *testing.T, database bool) []sdkConsumer {
	t.Helper()
	consumers := installedSDKConsumers(t, database)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	entry := os.Getenv("CUTMY_SDK_COLLECTION_JS_ENTRY")
	if info, err := os.Stat(entry); entry == "" || !filepath.IsAbs(entry) || err != nil || !info.Mode().IsRegular() {
		if os.Getenv("CUTMY_REQUIRE_SDK") == "1" {
			t.Fatal("CUTMY_SDK_COLLECTION_JS_ENTRY must identify the installed Node collection entry")
		}
		t.Skip("installed Node collection entry is not configured")
	}
	for i := range consumers {
		ext := ".py"
		if consumers[i].name == "typescript" {
			ext = ".mjs"
		}
		consumers[i].script = filepath.Join(root, "scripts", "sdk-collection-acceptance"+ext)
	}
	return consumers
}

// Killing a real consumer after its saved checkpoint makes recovery independent
// of an exception handler or a cooperative SDK shutdown.
func runSDKCollectionConsumer(t *testing.T, ctx context.Context, consumer sdkConsumer, action, input, work, base string, kill bool) sdkCollectionReport {
	t.Helper()
	cmd := exec.CommandContext(ctx, consumer.runtime, consumer.script, action, input, work, base)
	cmd.Env = os.Environ()
	if consumer.name == "python" && action == "local" {
		// Redirected Windows stdout may use cp1252; ASCII JSON must preserve Unicode labels.
		cmd.Env = append(cmd.Env, "PYTHONIOENCODING=cp1252")
	}
	stderr := &sdkOutputBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	retainDiagnostic := func() {
		if root := os.Getenv("CUTMY_SDK_COLLECTION_EVIDENCE"); filepath.IsAbs(root) {
			dir := filepath.Join(root, "private-diagnostics")
			if os.MkdirAll(dir, 0700) == nil {
				_ = os.WriteFile(filepath.Join(dir, consumer.name+"-"+action+".log"), stderr.buffer.Bytes(), 0600)
			}
		}
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(io.LimitReader(pipe, 4<<20))
	var report sdkCollectionReport
	if err = decoder.Decode(&report); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		retainDiagnostic()
		t.Fatalf("installed %s collection %s returned no bounded JSON report: %v", consumer.name, action, err)
	}
	if kill {
		if report.Event != "checkpoint" || report.Report == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("consumer did not reach its durable checkpoint")
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err == nil {
			t.Fatal("hard-stopped consumer exited successfully")
		}
		if action == "kill-eight" && (report.Checkpoint != "eight_ready" || len(report.Report.Outputs) != 8) {
			t.Fatal("consumer was not interrupted at exactly eight ready files")
		}
		if action == "kill-download" && report.Checkpoint != "download_interrupted" {
			t.Fatal("consumer did not checkpoint an incomplete transfer")
		}
		return *report.Report
	}
	trailing := decoder.Decode(new(any))
	if err = cmd.Wait(); err != nil {
		retainDiagnostic()
		t.Fatalf("installed %s collection %s failed: %v (raw diagnostics remain private)", consumer.name, action, err)
	}
	if !errors.Is(trailing, io.EOF) {
		t.Fatalf("consumer printed trailing data: %v", trailing)
	}
	return report
}

func sdkCollectionPlan(real bool) CollectionPlan {
	plan := CollectionPlan{Version: 1, Options: CollectionOptions{Format: "mp4", Quality: "best", CutMode: "accurate"}}
	for i := 0; i < 15; i++ {
		first := int64(200 + (i%6)*400)
		second := int64(200 + ((15-i)%6)*400)
		ranges := []Range{{StartMS: first, EndMS: first + 480, Label: "ответ"}, {StartMS: second, EndMS: second + 520, Label: "уточнение"}}
		if real {
			first = int64(20000 + i*30000)
			second = first + 10000
			ranges = []Range{{StartMS: first, EndMS: first + 6000, Label: "ответ"}, {StartMS: second, EndMS: second + 3000, Label: "уточнение"}}
			if i%2 == 1 {
				ranges[0], ranges[1] = ranges[1], ranges[0]
			}
		}
		clip := CollectionClip{ID: fmt.Sprintf("clip-%02d", i+1), Label: fmt.Sprintf("Фрагмент интервью %02d", i+1), Ranges: ranges}
		if i == 2 {
			clip.Options.Crop = &Crop{X: .25, Y: 0, Width: .5, Height: 1}
		}
		plan.Clips = append(plan.Clips, clip)
	}
	return plan
}

func sdkCollectionWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func sdkCollectionPrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sdkCollectionFingerprint(t *testing.T, output sdkCollectionOutput) []byte {
	t.Helper()
	info, err := os.Lstat(output.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != output.SizeBytes {
		t.Fatalf("invalid acknowledged collection file: %v", err)
	}
	raw, err := os.ReadFile(output.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if output.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("SDK receipt does not match independent output hash")
	}
	return raw
}

func sdkCollectionMatchesStoredArtifact(t *testing.T, ctx context.Context, store *Store, output sdkCollectionOutput) {
	t.Helper()
	var path, jobID string
	var size int64
	if err := store.DB.QueryRow(ctx, "SELECT path,job_id,size_bytes FROM artifacts WHERE id=$1", output.ArtifactID).Scan(&path, &jobID, &size); err != nil {
		t.Fatal("SDK receipt did not name an actual stored artifact")
	}
	raw, err := os.ReadFile(path)
	if err != nil || jobID != output.JobID || size != output.SizeBytes || !bytes.Equal(raw, sdkCollectionFingerprint(t, output)) {
		t.Fatal("downloaded SDK bytes differ from the backend's sealed artifact")
	}
}

func checkSDKCollectionMedia(t *testing.T, ctx context.Context, config Config, output sdkCollectionOutput, ranges []Range, width, height int) {
	t.Helper()
	sdkCollectionFingerprint(t, output)
	var duration int64
	for _, r := range ranges {
		duration += r.EndMS - r.StartMS
	}
	info, actual, err := probe(ctx, config, output.Path, false)
	if err != nil || actual < duration-120 || actual > duration+120 || len(info.Streams) != 2 {
		t.Fatalf("collection result changed duration/tracks: %d, %v", actual, err)
	}
	if len(output.Segments) != len(ranges) || len(output.TrackManifest) < 2 {
		t.Fatal("collection output lost source/output maps")
	}
	audioIndex := 2
	if height == 720 {
		audioIndex = 1
	}
	video, audio := false, false
	for _, track := range output.TrackManifest {
		if !track.Included {
			continue
		}
		if track.Type == "video" && track.Index == 0 {
			video = true
		} else if track.Type == "audio" && track.Index == audioIndex {
			audio = true
		} else {
			t.Fatal("collection included an unexpected source track")
		}
	}
	if !video || !audio {
		t.Fatal("collection lost the selected source video/audio tracks")
	}
	var cursor int64
	for i, s := range output.Segments {
		r := ranges[i]
		if s.SourceStartMS != r.StartMS || s.SourceEndMS < r.EndMS-100 || s.SourceEndMS > r.EndMS+100 || s.OutputStartMS != cursor || s.OutputEndMS < cursor+r.EndMS-r.StartMS-100 || s.OutputEndMS > cursor+r.EndMS-r.StartMS+100 {
			t.Fatalf("wrong clip range/order map: %+v want %+v", s, r)
		}
		cursor = s.OutputEndMS
	}
	if _, err = runCommand(ctx, config.FFmpeg, "-v", "error", "-xerror", "-i", output.Path, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-"); err != nil {
		t.Fatalf("collection media cannot independently decode: %v", err)
	}
	mcSquareGeometry(t, ctx, config, output.Path, width, height)
	if height == 96 {
		mcTone(t, mcPCM(t, ctx, config, output.Path, .2, .2, mcAudioIndex(t, mcProbe(t, ctx, config, output.Path))), 880)
	}
}

func TestInstalledSDKCollectionLocalWorkflow(t *testing.T) {
	for _, consumer := range sdkCollectionConsumers(t, false) {
		t.Run(consumer.name, func(t *testing.T) {
			config := mediaConfig(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			input := sdkMediaFixture(t, ctx, config)
			report := runSDKCollectionConsumer(t, ctx, consumer, "local", input, sdkCollectionPrivateDir(t), "", false)
			if !report.Complete || len(report.Outputs) != 5 {
				t.Fatal("installed local collection cycle did not return three finals and two selected drafts")
			}
			ranges := map[string][]Range{"opening": {{StartMS: 500, EndMS: 1500}}, "answer": {{StartMS: 2200, EndMS: 3000}, {StartMS: 200, EndMS: 1000}}, "vertical": {{StartMS: 1000, EndMS: 2000}}}
			for _, out := range report.Outputs {
				width := 160
				if out.ClipID == "vertical" {
					width = 80
				}
				checkSDKCollectionMedia(t, ctx, config, out, ranges[out.ClipID], width, 96)
				mcTone(t, mcPCM(t, ctx, config, out.Path, .2, .2, mcAudioIndex(t, mcProbe(t, ctx, config, out.Path))), 880)
			}
		})
	}
}

func TestInstalledSDKCollectionHTTPWorkflow(t *testing.T) {
	for _, consumer := range sdkCollectionConsumers(t, true) {
		t.Run(consumer.name, func(t *testing.T) {
			store := testStore(t)
			config := mediaConfig(t)
			config.PublicOrigin = "https://sdk-public.example"
			config.WorkerHealthPath = filepath.Join(t.TempDir(), "worker-health")
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
				if !filepath.IsAbs(input) {
					t.Fatal("real interview must be an absolute independently supplied file")
				}
			}
			inputBytes, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			inputHash := sha256.Sum256(inputBytes)
			inputSHA := hex.EncodeToString(inputHash[:])
			if real && (len(inputBytes) != 82242740 || inputSHA != "0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca") {
				t.Fatal("real interview does not match the independently frozen full-source master")
			}
			work := sdkCollectionPrivateDir(t)
			plan := sdkCollectionPlan(real)
			sdkCollectionWriteJSON(t, filepath.Join(work, "request.json"), map[string]any{"plan": plan, "rendition": "final"})
			revised := sdkCollectionPlan(real)
			revised.Clips[0].Ranges[0].StartMS += 40
			revised.Clips[14].Ranges[1].EndMS -= 40
			sdkCollectionWriteJSON(t, filepath.Join(work, "revised-request.json"), map[string]any{"plan": revised, "rendition": "final"})
			sdkCollectionWriteJSON(t, filepath.Join(work, "draft-request.json"), map[string]any{"plan": revised, "rendition": "draft", "clip_ids": []string{"clip-02", "clip-03"}})
			application := NewServer(config, store).Handler()
			// A real one-slot queue before the worker starts guarantees partial
			// admission independently of encoder speed. Later calls use normal limits.
			limitedConfig := config
			limitedConfig.MaxActiveJobs = 1
			initialAdmission := NewServer(limitedConfig, store).Handler()
			workerCtx, stopWorker := context.WithCancel(ctx)
			workerDone := make(chan error, 1)
			var workerOnce sync.Once
			startWorker := func() { workerOnce.Do(func() { go func() { workerDone <- RunWorker(workerCtx, config, store) }() }) }
			t.Cleanup(func() {
				stopWorker()
				workerOnce.Do(func() { workerDone <- nil })
				select {
				case err := <-workerDone:
					if err != nil {
						t.Error("collection worker shutdown failed")
					}
				case <-time.After(10 * time.Second):
					t.Error("collection worker did not stop")
				}
			})
			var mu sync.Mutex
			lostSubmit, brokenDownload, resumedRange, partialAdmission := false, false, false, false
			firstAdmission := true
			var brokenArtifact, brokenLastModified string
			var brokenBytes []byte
			var rangeOffset int64
			var firstAdmissionReply, nextAdmissionRequest time.Time
			brokenFullGets := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/collections" {
					rr := httptest.NewRecorder()
					mu.Lock()
					initial := firstAdmission
					firstAdmission = false
					if !initial && nextAdmissionRequest.IsZero() {
						nextAdmissionRequest = time.Now()
					}
					mu.Unlock()
					if initial {
						initialAdmission.ServeHTTP(rr, r)
					} else {
						application.ServeHTTP(rr, r)
					}
					startWorker()
					mu.Lock()
					var report CollectionSubmission
					_ = json.Unmarshal(rr.Body.Bytes(), &report)
					if initial && !report.Complete && rr.Header().Get("Retry-After") == "60" {
						firstAdmissionReply = time.Now()
					}
					drop := !lostSubmit && rr.Code == 202 && report.Complete
					if drop {
						lostSubmit = true
					}
					if len(report.Clips) > 0 && !report.Complete {
						partialAdmission = true
					}
					mu.Unlock()
					if drop {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
							return
						}
					}
					for k, vs := range rr.Header() {
						w.Header()[k] = vs
					}
					w.WriteHeader(rr.Code)
					_, _ = w.Write(rr.Body.Bytes())
					return
				}
				if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/artifacts/") && strings.HasSuffix(r.URL.Path, "/download") {
					rr := httptest.NewRecorder()
					application.ServeHTTP(rr, r)
					mu.Lock()
					drop := !brokenDownload && r.Header.Get("Range") == "" && rr.Code == 200 && rr.Body.Len() > 2
					if drop {
						brokenDownload = true
						brokenArtifact = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/"), "/download")
						brokenBytes = append([]byte(nil), rr.Body.Bytes()...)
						brokenLastModified = rr.Header().Get("Last-Modified")
					}
					if r.URL.Path == "/api/v1/artifacts/"+brokenArtifact+"/download" {
						if r.Header.Get("Range") == "" {
							brokenFullGets++
						}
						if rr.Code == 206 {
							offset, parseErr := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-"), 10, 64)
							if parseErr == nil && offset > 0 && offset < int64(len(brokenBytes)) && r.Header.Get("If-Range") == brokenLastModified && brokenLastModified != "" && rr.Header().Get("Content-Range") == fmt.Sprintf("bytes %d-%d/%d", offset, len(brokenBytes)-1, len(brokenBytes)) && bytes.Equal(rr.Body.Bytes(), brokenBytes[offset:]) {
								resumedRange, rangeOffset = true, offset
							}
						}
					}
					mu.Unlock()
					if drop {
						conn, buffer, err := w.(http.Hijacker).Hijack()
						if err == nil {
							defer conn.Close()
							fmt.Fprintf(buffer, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nContent-Type: %s\r\nLast-Modified: %s\r\nConnection: close\r\n\r\n", rr.Body.Len(), rr.Header().Get("Content-Type"), rr.Header().Get("Last-Modified"))
							_, _ = buffer.Write(rr.Body.Bytes()[:rr.Body.Len()/2])
							_ = buffer.Flush()
							return
						}
					}
					for k, vs := range rr.Header() {
						w.Header()[k] = vs
					}
					w.WriteHeader(rr.Code)
					_, _ = w.Write(rr.Body.Bytes())
					return
				}
				application.ServeHTTP(w, r)
			}))
			defer server.Close()
			prepared := runSDKCollectionConsumer(t, ctx, consumer, "prepare", input, work, server.URL, false)
			if prepared.SourceID == "" {
				t.Fatal("SDK preparation did not return its uploaded source identity")
			}
			var uploadedPath string
			if err := store.DB.QueryRow(ctx, "SELECT path FROM sources WHERE id=$1", prepared.SourceID).Scan(&uploadedPath); err != nil {
				t.Fatal(err)
			}
			uploadedBytes, err := os.ReadFile(uploadedPath)
			if err != nil || !bytes.Equal(inputBytes, uploadedBytes) {
				t.Fatal("installed SDK did not upload the complete unchanged interview")
			}
			sdkCollectionWriteJSON(t, filepath.Join(work, "request.json"), map[string]any{"source_id": prepared.SourceID, "plan": plan, "rendition": "final"})
			sdkCollectionWriteJSON(t, filepath.Join(work, "revised-request.json"), map[string]any{"source_id": prepared.SourceID, "plan": revised, "rendition": "final"})
			sdkCollectionWriteJSON(t, filepath.Join(work, "draft-request.json"), map[string]any{"source_id": prepared.SourceID, "plan": revised, "rendition": "draft", "clip_ids": []string{"clip-02", "clip-03"}})
			runSDKCollectionConsumer(t, ctx, consumer, "validate", input, work, server.URL, false)
			t.Log("installed collection validation passed without creating jobs")
			var count int
			if err := store.DB.QueryRow(ctx, "SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 0 {
				t.Fatal("validation queued collection work")
			}
			partial := runSDKCollectionConsumer(t, ctx, consumer, "kill-download", input, work, server.URL, true)
			mu.Lock()
			validPrefix := partial.Download != nil && partial.Download.ArtifactID == brokenArtifact && partial.Download.Offset > 0 && partial.Download.Offset < int64(len(brokenBytes)) && partial.Download.TotalSize == int64(len(brokenBytes))
			mu.Unlock()
			if !validPrefix {
				t.Fatal("hard-stopped SDK did not save the actually interrupted artifact prefix")
			}
			t.Log("hard-stopped installed consumer with a saved interrupted download prefix")
			eight := runSDKCollectionConsumer(t, ctx, consumer, "kill-eight", input, work, server.URL, true)
			t.Log("hard-stopped fresh consumer after exactly eight verified completed files")
			prior := map[string]sdkCollectionOutput{}
			for _, o := range eight.Outputs {
				sdkCollectionFingerprint(t, o)
				prior[o.ClipID] = o
			}
			final := runSDKCollectionConsumer(t, ctx, consumer, "resume", input, work, server.URL, false)
			if !final.Complete || len(final.Outputs) != 15 {
				t.Fatal("collection did not resume to all fifteen files")
			}
			byID := map[string]sdkCollectionOutput{}
			for i, out := range final.Outputs {
				sdkCollectionMatchesStoredArtifact(t, ctx, store, out)
				if out.ClipID != plan.Clips[i].ID {
					t.Fatal("collection output order does not follow the plan")
				}
				if old, ok := prior[out.ClipID]; ok && (old.JobID != out.JobID || old.SHA256 != out.SHA256 || old.Path != out.Path) {
					t.Fatal("resume replaced a previously completed clip")
				}
				w := width
				if i == 2 {
					w = width / 2
				}
				checkSDKCollectionMedia(t, ctx, config, out, plan.Clips[i].Ranges, w, height)
				byID[out.ClipID] = out
			}
			checkJobs := func(want int) {
				t.Helper()
				var jobs, artifacts, attempts int
				if err := store.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM jobs),(SELECT count(*) FROM artifacts),(SELECT COALESCE(sum(attempts),0) FROM jobs)").Scan(&jobs, &artifacts, &attempts); err != nil || jobs != want || artifacts != want || attempts != want {
					t.Fatalf("duplicate/incomplete render work: jobs=%d artifacts=%d attempts=%d want=%d err=%v", jobs, artifacts, attempts, want, err)
				}
			}
			checkJobs(15)
			t.Log("fresh-process resume completed fifteen files with exactly fifteen render attempts")
			replay := runSDKCollectionConsumer(t, ctx, consumer, "replay", input, work, server.URL, false)
			if !replay.Complete || len(replay.Outputs) != 15 {
				t.Fatal("unchanged replay lost results")
			}
			if !reflect.DeepEqual(replay.Outputs, final.Outputs) {
				t.Fatal("unchanged replay changed output order or receipts")
			}
			checkJobs(15)
			changed := runSDKCollectionConsumer(t, ctx, consumer, "revise-two", input, work, server.URL, false)
			if !changed.Complete || len(changed.Outputs) != 15 {
				t.Fatal("revised collection is incomplete")
			}
			checkJobs(17)
			t.Log("revising two clips created exactly two additional jobs")
			for i, out := range changed.Outputs {
				if out.ClipID != revised.Clips[i].ID {
					t.Fatal("revised output IDs/order differ from the plan")
				}
				sdkCollectionMatchesStoredArtifact(t, ctx, store, out)
				old := byID[out.ClipID]
				if i == 0 || i == 14 {
					if out.JobID == old.JobID || out.ArtifactID == old.ArtifactID || out.Path == old.Path {
						t.Fatal("edited clip reused old work")
					}
					checkSDKCollectionMedia(t, ctx, config, out, revised.Clips[i].Ranges, width, height)
				} else if out.JobID != old.JobID || out.SHA256 != old.SHA256 || out.Path != old.Path {
					t.Fatal("editing two clips changed unrelated output")
				}
			}
			draft := runSDKCollectionConsumer(t, ctx, consumer, "draft-selected", input, work, server.URL, false)
			if !draft.Complete || len(draft.Outputs) != 2 {
				t.Fatal("selected draft did not produce two clips")
			}
			checkJobs(19)
			t.Log("selected drafts produced two separate jobs and preserved final outputs")
			for i, out := range draft.Outputs {
				if out.ClipID != []string{"clip-02", "clip-03"}[i] {
					t.Fatal("selected draft lost unique ordered clip IDs")
				}
				sdkCollectionMatchesStoredArtifact(t, ctx, store, out)
				index := 1
				if out.ClipID == "clip-03" {
					index = 2
				}
				w := width
				if index == 2 {
					w = width / 2
				}
				checkSDKCollectionMedia(t, ctx, config, out, revised.Clips[index].Ranges, w, height)
				if old := byID[out.ClipID]; out.JobID == old.JobID || out.ArtifactID == old.ArtifactID || out.Path == old.Path {
					t.Fatal("draft and final identities collided")
				}
			}
			if draft.Outputs[0].JobID == draft.Outputs[1].JobID || draft.Outputs[0].ArtifactID == draft.Outputs[1].ArtifactID || draft.Outputs[0].Path == draft.Outputs[1].Path {
				t.Fatal("selected draft outputs share an identity")
			}
			backToFinal := runSDKCollectionConsumer(t, ctx, consumer, "revise-two", input, work, server.URL, false)
			if !backToFinal.Complete || !reflect.DeepEqual(backToFinal.Outputs, changed.Outputs) {
				t.Fatal("returning from selected draft changed retained final output receipts")
			}
			checkJobs(19)
			t.Log("returning from selected drafts restored all fifteen finals without new renders")
			mu.Lock()
			l, b, r, p := lostSubmit, brokenDownload, resumedRange, partialAdmission
			admissionBackoff := !firstAdmissionReply.IsZero() && !nextAdmissionRequest.IsZero() && nextAdmissionRequest.Sub(firstAdmissionReply) >= 59500*time.Millisecond
			mu.Unlock()
			if !l || !b || !r || !p || !admissionBackoff || brokenFullGets != 1 || rangeOffset != partial.Download.Offset {
				t.Fatalf("required failure/admission path was not exercised: lost=%t broken=%t range=%t partial=%t", l, b, r, p)
			}
			finalInput, err := os.ReadFile(input)
			if err != nil || !bytes.Equal(inputBytes, finalInput) {
				t.Fatal("collection changed the supplied source file")
			}
			if root := os.Getenv("CUTMY_SDK_COLLECTION_EVIDENCE"); root != "" {
				if !filepath.IsAbs(root) {
					t.Fatal("collection evidence must be outside the checkout")
				}
				dir := filepath.Join(root, consumer.name)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				sdkCollectionWriteJSON(t, filepath.Join(dir, "requests.json"), map[string]any{
					"initial": map[string]any{"plan": plan, "rendition": "final"},
					"revised": map[string]any{"plan": revised, "rendition": "final"},
					"draft":   map[string]any{"plan": revised, "rendition": "draft", "clip_ids": []string{"clip-02", "clip-03"}},
				})
				if !real {
					if err := os.WriteFile(filepath.Join(dir, "source.mp4"), inputBytes, 0600); err != nil {
						t.Fatal(err)
					}
				}
				mediaDir := filepath.Join(dir, "media")
				if err := os.MkdirAll(mediaDir, 0700); err != nil {
					t.Fatal(err)
				}
				copied := make(map[string]string)
				for _, report := range []*sdkCollectionReport{&final, &changed, &draft} {
					for i := range report.Outputs {
						out := &report.Outputs[i]
						if path, ok := copied[out.ArtifactID]; ok {
							out.Path = path
							continue
						}
						if filepath.Base(out.ArtifactID) != out.ArtifactID || out.ArtifactID == "" {
							t.Fatal("unsafe evidence artifact ID")
						}
						path := filepath.Join(mediaDir, out.ClipID+"."+out.ArtifactID+".mp4")
						if err := os.WriteFile(path, sdkCollectionFingerprint(t, *out), 0600); err != nil {
							t.Fatal(err)
						}
						copied[out.ArtifactID] = path
						out.Path = path
					}
				}
				receipt := map[string]any{"passed": true, "real_interview": real, "source_sha256": inputSHA, "source_size_bytes": len(inputBytes), "source_width": width, "source_height": height, "clips": 15, "completed_before_hard_kill": 8, "jobs": 19, "artifacts": 19, "render_attempts": 19, "lost_submit_reply_recovered": l, "interrupted_download_ranged": r, "resumed_offset": rangeOffset, "interrupted_artifact_full_gets": brokenFullGets, "partial_admission": p, "admission_retry_after_respected": admissionBackoff, "final_restored_without_new_work": true, "initial": final, "revised": changed, "draft": draft}
				sdkCollectionWriteJSON(t, filepath.Join(dir, "acceptance.json"), receipt)
			}
		})
	}
}
