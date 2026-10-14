package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// These acceptance tests import the installed wheel/tarball, rather than the
// SDK source tree. The CI requirement flag makes missing prerequisites fail.
type sdkConsumer struct {
	name, runtime, script string
}

func installedSDKConsumers(t *testing.T, database bool) []sdkConsumer {
	t.Helper()
	var missing []string
	for _, name := range []string{"CUTMY_SDK_PYTHON", "CUTMY_SDK_JS_ENTRY", "CUTMY_SDK_LOCAL_JS_ENTRY", "CUTMY_SDK_BINARY"} {
		path := os.Getenv(name)
		if path == "" || !filepath.IsAbs(path) {
			missing = append(missing, name+" must identify an installed absolute path")
			continue
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			missing = append(missing, name+" must identify a regular file")
		}
	}
	node := os.Getenv("CUTMY_SDK_NODE")
	if node == "" {
		node = "node"
	}
	for _, tool := range []string{node, "ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, "missing runtime/tool "+tool)
		}
	}
	if database && os.Getenv("TEST_DATABASE_URL") == "" {
		missing = append(missing, "HTTP workflow requires TEST_DATABASE_URL")
	}
	if len(missing) > 0 {
		message := strings.Join(missing, "; ")
		if os.Getenv("CUTMY_REQUIRE_SDK") == "1" {
			t.Fatal(message)
		}
		t.Skip(message)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return []sdkConsumer{
		{"python", os.Getenv("CUTMY_SDK_PYTHON"), filepath.Join(root, "scripts", "sdk-smoke.py")},
		{"typescript", node, filepath.Join(root, "scripts", "sdk-smoke.mjs")},
	}
}

type sdkSmokeOutput struct {
	Path     string   `json:"path"`
	Artifact Artifact `json:"artifact"`
}

type sdkSmokeReport struct {
	Mode      string           `json:"mode"`
	SourceID  string           `json:"source_id"`
	JobID     string           `json:"job_id"`
	ReplayID  string           `json:"replay_id"`
	Preflight PreflightResult  `json:"preflight"`
	Predicted []string         `json:"predicted_paths"`
	Outputs   []sdkSmokeOutput `json:"outputs"`
	Job       Job              `json:"job"`
}

type sdkOutputBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *sdkOutputBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, errors.New("SDK consumer output exceeded its limit")
	}
	return b.buffer.Write(p)
}

func runSDKConsumer(t *testing.T, ctx context.Context, consumer sdkConsumer, mode, input, output, baseURL string) sdkSmokeReport {
	t.Helper()
	command := exec.CommandContext(ctx, consumer.runtime, consumer.script, mode, input, output, baseURL)
	command.Env = os.Environ()
	stdout, stderr := &sdkOutputBuffer{limit: 1 << 20}, &sdkOutputBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		t.Fatalf("installed %s %s consumer failed: %v\n%s", consumer.name, mode, err, stderr.buffer.String())
	}
	var report sdkSmokeReport
	decoder := json.NewDecoder(&stdout.buffer)
	if err := decoder.Decode(&report); err != nil || report.Mode != mode {
		t.Fatalf("invalid %s SDK consumer report: %s (%v)", consumer.name, stdout.buffer.String(), err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("%s SDK consumer printed trailing output: %v", consumer.name, err)
	}
	return report
}

func sdkMediaFixture(t *testing.T, ctx context.Context, config Config) string {
	t.Helper()
	fixture := filepath.Join(config.DataDir, "sdk-input.mp4")
	mcRun(t, ctx, config, "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-t", "4", "-map", "0:v:0", "-map", "1:a:0", "-map", "2:a:0",
		"-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-g", "25", "-bf", "0", "-c:a", "aac",
		"-disposition:a:0", "0", "-disposition:a:1", "default", "-movflags", "+faststart", fixture)
	return fixture
}

func checkSDKMedia(t *testing.T, ctx context.Context, config Config, output sdkSmokeOutput, duration int64, ranges []Range, firstIndex int) {
	t.Helper()
	info, actualDuration, err := probe(ctx, config, output.Path, false)
	if err != nil || actualDuration < duration-100 || actualDuration > duration+100 || len(info.Streams) != 2 {
		t.Fatalf("SDK result changed media bounds/tracks: duration=%d, streams=%d, err=%v", actualDuration, len(info.Streams), err)
	}
	file, err := os.Stat(output.Path)
	if err != nil || file.Size() <= 0 || file.Size() != output.Artifact.SizeBytes {
		t.Fatalf("SDK result differs from acknowledged artifact size: %v (%+v)", err, output.Artifact)
	}
	if output.Artifact.Segments == nil || len(*output.Artifact.Segments) != len(ranges) || output.Artifact.TrackManifest == nil {
		t.Fatalf("SDK result lost manifests: %+v", output.Artifact)
	}
	var offset int64
	for index, segment := range *output.Artifact.Segments {
		selected := ranges[index]
		if segment.Index != firstIndex+index || segment.SourceStartMS != selected.StartMS || segment.SourceEndMS < selected.EndMS-100 || segment.SourceEndMS > selected.EndMS+100 || segment.OutputStartMS != offset || segment.OutputEndMS < offset+selected.EndMS-selected.StartMS-100 || segment.OutputEndMS > offset+selected.EndMS-selected.StartMS+100 {
			t.Fatalf("SDK segment changed range/order: %+v, want %+v at %d", segment, selected, offset)
		}
		offset = segment.OutputEndMS
	}
	selectedVideo, selectedAudio := false, false
	for _, track := range *output.Artifact.TrackManifest {
		if track.Included && track.Type == "video" && track.Index == 0 {
			selectedVideo = true
		}
		if track.Included && track.Type == "audio" && track.Index == 2 {
			selectedAudio = true
		}
	}
	if !selectedVideo || !selectedAudio {
		t.Fatalf("SDK result lost selected video/default second audio: %+v", *output.Artifact.TrackManifest)
	}
	if _, err := runCommand(ctx, config.FFmpeg, "-v", "error", "-i", output.Path, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-"); err != nil {
		t.Fatalf("SDK output cannot be fully decoded: %v", err)
	}
	mcSquareGeometry(t, ctx, config, output.Path, 160, 96)
	mcTone(t, mcPCM(t, ctx, config, output.Path, .2, .2, mcAudioIndex(t, mcProbe(t, ctx, config, output.Path))), 880)
}

func TestInstalledSDKLocalWorkflow(t *testing.T) {
	consumers := installedSDKConsumers(t, false)
	for _, consumer := range consumers {
		t.Run(consumer.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			config := mediaConfig(t)
			input := sdkMediaFixture(t, ctx, config)
			output := t.TempDir()
			report := runSDKConsumer(t, ctx, consumer, "local", input, output, "")
			if !report.Preflight.Valid || report.Preflight.OutputCount != 2 || report.Preflight.RequestedMS != 2000 || len(report.Predicted) != 2 || len(report.Outputs) != 2 {
				t.Fatalf("SDK local report lost preflight/result: %+v", report)
			}
			for index, start := range []int64{500, 2000} {
				if report.Outputs[index].Path != report.Predicted[index] || filepath.Dir(report.Outputs[index].Path) != output || report.Outputs[index].Artifact.ActualStartMS != start || report.Outputs[index].Artifact.ActualEndMS < start+900 || report.Outputs[index].Artifact.ActualEndMS > start+1100 {
					t.Fatalf("SDK local result differs from planned path/bounds: %+v", report.Outputs[index])
				}
				checkSDKMedia(t, ctx, config, report.Outputs[index], 1000, []Range{{StartMS: start, EndMS: start + 1000}}, index)
			}
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 2 {
				t.Fatalf("SDK local workflow retained temporary files: entries=%d err=%v", len(entries), err)
			}
		})
	}
}

type sdkFileState struct {
	Path string
	Size int64
	Mode fs.FileMode
}

func sdkFileInventory(root string) ([]sdkFileState, error) {
	var inventory []sdkFileState
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			inventory = append(inventory, sdkFileState{path, info.Size(), info.Mode()})
		}
		return nil
	})
	return inventory, err
}

func TestInstalledSDKHTTPWorkflow(t *testing.T) {
	consumers := installedSDKConsumers(t, true)
	for _, consumer := range consumers {
		t.Run(consumer.name, func(t *testing.T) {
			store := testStore(t)
			config := mediaConfig(t)
			// Private API addresses can differ from the public browser origin.
			config.PublicOrigin = "https://sdk-public.example"
			config.WorkerHealthPath = filepath.Join(t.TempDir(), "worker-health")
			config.WorkerConcurrency, config.FFmpegThreads = 1, 1
			config.SourceTimeout, config.UploadTimeout, config.JobTimeout = 20*time.Second, 20*time.Second, 30*time.Second
			config.SourceTTL, config.ArtifactTTL, config.StorageWaitTimeout = time.Hour, time.Hour, time.Minute
			config.MaxStorageBytes, config.MaxOwnerBytes, config.MaxActiveJobs, config.MutationsPerMinute = 1<<30, 1<<30, 32, 100
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			input := sdkMediaFixture(t, ctx, config)
			application := NewServer(config, store).Handler()
			workerCtx, stopWorker := context.WithCancel(ctx)
			var startWorker sync.Once
			workerDone := make(chan error, 1)
			var mu sync.Mutex
			var preflightChecked, workerStarted bool
			var verificationErrors []error
			recordError := func(err error) {
				if err != nil {
					mu.Lock()
					verificationErrors = append(verificationErrors, err)
					mu.Unlock()
				}
			}
			t.Cleanup(func() {
				stopWorker()
				mu.Lock()
				started := workerStarted
				mu.Unlock()
				if started {
					select {
					case err := <-workerDone:
						if err != nil {
							t.Errorf("SDK acceptance worker shutdown: %v", err)
						}
					case <-time.After(10 * time.Second):
						t.Error("SDK acceptance worker did not stop")
					}
				}
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				isPreflight := r.Method == http.MethodPost && r.URL.Path == "/api/v1/jobs/preflight"
				var before []sdkFileState
				if isPreflight {
					var err error
					before, err = sdkFileInventory(config.DataDir)
					recordError(err)
				}
				application.ServeHTTP(w, r)
				if isPreflight {
					var jobs, artifacts, reservations, readers int
					err := store.DB.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM jobs),(SELECT count(*) FROM artifacts),(SELECT count(*) FROM storage_reservations),(SELECT count(*) FROM source_media_readers)`).Scan(&jobs, &artifacts, &reservations, &readers)
					recordError(err)
					if jobs != 0 || artifacts != 0 || reservations != 0 || readers != 0 {
						recordError(fmt.Errorf("SDK preflight created execution state: jobs=%d artifacts=%d reservations=%d readers=%d", jobs, artifacts, reservations, readers))
					}
					after, err := sdkFileInventory(config.DataDir)
					recordError(err)
					if !reflect.DeepEqual(before, after) {
						recordError(errors.New("SDK preflight created or changed media files"))
					}
					mu.Lock()
					preflightChecked = true
					mu.Unlock()
				}
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/jobs" {
					startWorker.Do(func() {
						mu.Lock()
						workerStarted = true
						mu.Unlock()
						go func() { workerDone <- RunWorker(workerCtx, config, store) }()
					})
				}
			}))
			defer server.Close()
			report := runSDKConsumer(t, ctx, consumer, "http", input, t.TempDir(), server.URL)
			mu.Lock()
			checked, checkErrors := preflightChecked, append([]error(nil), verificationErrors...)
			mu.Unlock()
			if !checked || len(checkErrors) != 0 {
				t.Fatalf("SDK preflight was not read-only: checked=%t errors=%v", checked, checkErrors)
			}
			if !report.Preflight.Valid || report.Preflight.OutputCount != 1 || report.Preflight.RequestedMS != 2000 || report.SourceID == "" || report.Preflight.Request.SourceID != report.SourceID || report.JobID == "" || report.JobID != report.ReplayID || report.Job.ID != report.JobID || report.Job.Status != "succeeded" || len(report.Job.Items) != 1 || len(report.Outputs) != 1 {
				t.Fatalf("SDK HTTP report lost preflight/idempotency/result: %+v", report)
			}
			var jobs, artifacts int
			if err := store.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM jobs),(SELECT count(*) FROM artifacts)").Scan(&jobs, &artifacts); err != nil || jobs != 1 || artifacts != 1 {
				t.Fatalf("SDK retry created duplicate work: jobs=%d artifacts=%d err=%v", jobs, artifacts, err)
			}
			if report.Job.Items[0].Artifact == nil || report.Outputs[0].Artifact.ID != report.Job.Items[0].Artifact.ID {
				t.Fatal("SDK downloaded a result that differs from the terminal job")
			}
			var storedPath string
			if err := store.DB.QueryRow(ctx, "SELECT path FROM artifacts WHERE id=$1", report.Outputs[0].Artifact.ID).Scan(&storedPath); err != nil {
				t.Fatal(err)
			}
			storedBytes, err := os.ReadFile(storedPath)
			if err != nil {
				t.Fatal(err)
			}
			downloadedBytes, err := os.ReadFile(report.Outputs[0].Path)
			if err != nil || !bytes.Equal(storedBytes, downloadedBytes) {
				t.Fatalf("SDK download changed the published artifact bytes: %v", err)
			}
			checkSDKMedia(t, ctx, config, report.Outputs[0], 2000, []Range{{StartMS: 500, EndMS: 1500}, {StartMS: 2000, EndMS: 3000}}, 0)
		})
	}
}
