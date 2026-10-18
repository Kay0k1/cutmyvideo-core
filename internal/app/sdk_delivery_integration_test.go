package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"math"
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

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

type sdkDeliveryOutput struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Artifact struct {
		SizeBytes int64            `json:"size_bytes"`
		Segments  []engine.Segment `json:"segments"`
	} `json:"artifact"`
}

type sdkDeliveryReport struct {
	Mode       string              `json:"mode"`
	Outputs    []sdkDeliveryOutput `json:"outputs"`
	Collection []sdkDeliveryOutput `json:"collection"`
	Poster     struct {
		Path string `json:"path"`
	} `json:"poster"`
	Chapters      string `json:"chapters"`
	Portable      bool   `json:"portable_roundtrip"`
	AffineSidecar bool   `json:"affine_sidecar"`
}

func sdkDeliveryDuration(t *testing.T, ctx context.Context, c Config, path string) float64 {
	t.Helper()
	raw, err := runCommand(ctx, c.FFprobe, "-v", "error", "-show_entries", "format=duration", "-of", "json", path)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) {
		t.Fatalf("invalid independently probed duration %q", result.Format.Duration)
	}
	return duration
}

func sdkDeliveryPackets(t *testing.T, ctx context.Context, c Config, path string) []string {
	t.Helper()
	raw, err := runCommand(ctx, c.FFprobe, "-v", "error", "-select_streams", "a:0", "-show_packets", "-show_entries", "packet=data_hash", "-show_data_hash", "sha256", "-of", "json", path)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Packets []struct {
			Hash string `json:"data_hash"`
		} `json:"packets"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, packet := range result.Packets {
		if !strings.HasPrefix(packet.Hash, "SHA256:") {
			t.Fatal("missing packet hash")
		}
		hashes = append(hashes, packet.Hash)
	}
	return hashes
}

func checkSDKDelivery(t *testing.T, ctx context.Context, c Config, source string, report sdkDeliveryReport) {
	t.Helper()
	if !report.Portable || !report.AffineSidecar || len(report.Outputs) != 4 || len(report.Collection) != 2 {
		t.Fatalf("incomplete installed delivery receipt: %+v", report)
	}
	seen := map[string]bool{}
	for _, out := range report.Outputs {
		if seen[out.Name] {
			t.Fatal("duplicate delivery output")
		}
		seen[out.Name] = true
		info, err := os.Stat(out.Path)
		if err != nil || info.Size() != out.Artifact.SizeBytes || len(out.Artifact.Segments) == 0 {
			t.Fatalf("invalid delivery publication: %+v %v", out, err)
		}
		mcRun(t, ctx, c, "-xerror", "-i", out.Path, "-f", "null", "-")
		streams := mcProbe(t, ctx, c, out.Path)
		if out.Name != "speed-target" {
			codec := map[string]string{"wav": "pcm_s24le", "flac": "flac", "m4a": "aac"}[out.Name]
			if len(streams) != 1 || streams[0].CodecType != "audio" || streams[0].CodecName != codec {
				t.Fatalf("wrong audio-only codec/selection %+v", streams)
			}
			if math.Abs(sdkDeliveryDuration(t, ctx, c, out.Path)-4) > .1 {
				t.Fatal("audio output changed selected duration")
			}
		}
		switch out.Name {
		case "wav", "flac":
			reference := mcRun(t, ctx, c, "-i", source, "-map", "0:a:0", "-t", "4", "-c:a", "pcm_s24le", "-f", "s24le", "-")
			decoded := mcRun(t, ctx, c, "-i", out.Path, "-map", "0:a:0", "-t", "4", "-c:a", "pcm_s24le", "-f", "s24le", "-")
			if len(decoded) != 48000*4*3 || !bytes.Equal(decoded, reference) {
				t.Fatalf("%s changed independently decoded PCM24 samples/rate/channels (%d/%d)", out.Name, len(decoded), len(reference))
			}
		case "m4a":
			original, copied := sdkDeliveryPackets(t, ctx, c, source), sdkDeliveryPackets(t, ctx, c, out.Path)
			matched := false
			for i := 0; i+len(copied) <= len(original); i++ {
				if reflect.DeepEqual(original[i:i+len(copied)], copied) {
					matched = true
					break
				}
			}
			if len(copied) < 150 || !matched {
				t.Fatal("AAC extraction reencoded or changed packet payloads")
			}
		case "speed-target":
			if info.Size() > 262144 || math.Abs(sdkDeliveryDuration(t, ctx, c, out.Path)-2.8) > .1 {
				t.Fatal("target-size/speed output differs from byte cap or duration")
			}
			want := []engine.Segment{{Index: 0, Label: "Blue", SourceStartMS: 2000, SourceEndMS: 4000, OutputStartMS: 0, OutputEndMS: 1600}, {Index: 1, Label: "Red", SourceStartMS: 0, SourceEndMS: 1000, OutputStartMS: 1600, OutputEndMS: 2400}, {Index: 2, Label: "Blue again", SourceStartMS: 2000, SourceEndMS: 2500, OutputStartMS: 2400, OutputEndMS: 2800}}
			if !reflect.DeepEqual(out.Artifact.Segments, want) {
				t.Fatalf("source/output clock map changed: %+v", out.Artifact.Segments)
			}
			mcSquareGeometry(t, ctx, c, out.Path, 160, 96)
			mcTone(t, mcPCM(t, ctx, c, out.Path, .2, .5, mcAudioIndex(t, streams)), 440)
		default:
			t.Fatal("unknown delivery operation", out.Name)
		}
	}
	for _, name := range []string{"wav", "flac", "m4a", "speed-target"} {
		if !seen[name] {
			t.Fatal("missing", name)
		}
	}
	for _, out := range report.Collection {
		want := 1.6
		if out.Name == "reset" {
			want = 1
		} else if out.Name != "inherit" {
			t.Fatal("unknown inherited output")
		}
		if math.Abs(sdkDeliveryDuration(t, ctx, c, out.Path)-want) > .1 {
			t.Fatal("collection inheritance/reset changed playback clock")
		}
		mcRun(t, ctx, c, "-xerror", "-i", out.Path, "-f", "null", "-")
	}
	f, err := os.Open(report.Poster.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, format, err := image.Decode(f)
	if err != nil || format != "png" || im.Bounds().Dx() != 160 || im.Bounds().Dy() != 96 {
		t.Fatalf("poster dimensions/decode failed: %v", err)
	}
	r, g, b, _ := im.At(80, 48).RGBA()
	// At output2s the reordered speed-adjusted timeline is RED. Original2s is BLUE.
	if r < 45000 || g > 15000 || b > 15000 {
		t.Fatalf("poster used original source clock instead of finished output: %v/%v/%v", r, g, b)
	}
	for _, clock := range []string{"START=1600", "START=2400", "END=2800"} {
		if !strings.Contains(report.Chapters, clock) {
			t.Fatal("missing actual output chapter", clock)
		}
	}
}

func runSDKDelivery(t *testing.T, ctx context.Context, consumer sdkConsumer, mode, input, audio, output, base string) sdkDeliveryReport {
	t.Helper()
	extension := ".py"
	if consumer.name == "typescript" {
		extension = ".mjs"
	}
	command := exec.CommandContext(ctx, consumer.runtime, filepath.Join(filepath.Dir(consumer.script), "sdk-delivery-acceptance"+extension), mode, input, audio, output, base)
	command.Env = os.Environ()
	stdout, stderr := &sdkOutputBuffer{limit: 1 << 20}, &sdkOutputBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		t.Fatalf("installed %s %s delivery: %v\n%s", consumer.name, mode, err, stderr.buffer.String())
	}
	var report sdkDeliveryReport
	if err := json.Unmarshal(stdout.buffer.Bytes(), &report); err != nil || report.Mode != mode {
		t.Fatalf("invalid delivery report: %v %s", err, stdout.buffer.String())
	}
	return report
}

func TestInstalledSDKDeliveryLocalWorkflow(t *testing.T) {
	for _, consumer := range installedSDKConsumers(t, false) {
		t.Run(consumer.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			c := mediaConfig(t)
			input, audio := sdkFeatureFixture(t, ctx, c)
			checkSDKDelivery(t, ctx, c, input, runSDKDelivery(t, ctx, consumer, "local", input, audio, t.TempDir(), ""))
		})
	}
}

func TestInstalledSDKDeliveryHTTPWorkflow(t *testing.T) {
	for _, consumer := range installedSDKConsumers(t, true) {
		t.Run(consumer.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
			defer cancel()
			store := testStore(t)
			c := mediaConfig(t)
			c.PublicOrigin = "https://sdk-public.example"
			c.WorkerHealthPath = filepath.Join(t.TempDir(), "worker-health")
			c.WorkerConcurrency, c.FFmpegThreads = 1, 1
			c.SourceTimeout, c.UploadTimeout, c.JobTimeout = 30*time.Second, 30*time.Second, 90*time.Second
			c.SourceTTL, c.ArtifactTTL, c.StorageWaitTimeout = time.Hour, time.Hour, time.Minute
			c.MaxStorageBytes, c.MaxOwnerBytes, c.MaxActiveJobs, c.MutationsPerMinute = 1<<30, 1<<30, 32, 1000
			input, audio := sdkFeatureFixture(t, ctx, c)
			handler := NewServer(c, store).Handler()
			var mu sync.Mutex
			submitted := map[string][][]byte{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && r.URL.Path == "/api/v1/jobs" {
					data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
					if err != nil {
						t.Error(err)
					}
					r.Body = io.NopCloser(bytes.NewReader(data))
					mu.Lock()
					key := r.Header.Get("Idempotency-Key")
					submitted[key] = append(submitted[key], data)
					mu.Unlock()
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			workerCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- RunWorker(workerCtx, c, store) }()
			defer func() {
				stop()
				select {
				case err := <-done:
					if err != nil && !strings.Contains(err.Error(), "context canceled") {
						t.Error(fmt.Errorf("SDK worker shutdown: %w", err))
					}
				case <-time.After(10 * time.Second):
					t.Error("SDK worker did not stop")
				}
			}()
			checkSDKDelivery(t, ctx, c, input, runSDKDelivery(t, ctx, consumer, "http", input, audio, t.TempDir(), server.URL))
			mu.Lock()
			defer mu.Unlock()
			if len(submitted) != 4 {
				t.Fatalf("unexpected direct submission count %d", len(submitted))
			}
			for key, bodies := range submitted {
				if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
					t.Fatalf("request body changed on exact idempotent replay %s", key)
				}
			}
		})
	}
}
