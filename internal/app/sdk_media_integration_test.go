package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

// These installed consumers must produce real media. The assertions below use
// known source clocks, independently decoded pixels/PCM and ffprobe geometry.
type sdkFeatureReport struct {
	Mode     string                     `json:"mode"`
	Silence  engine.SilenceResult       `json:"silence"`
	Video    engine.VideoAnalysisResult `json:"video"`
	Loudness engine.LoudnessResult      `json:"loudness"`
	Outputs  []struct {
		Name     string   `json:"name"`
		Path     string   `json:"path"`
		Artifact Artifact `json:"artifact"`
	} `json:"outputs"`
}

func sdkFeatureFixture(t *testing.T, ctx context.Context, c Config) (string, string) {
	t.Helper()
	input, audio := filepath.Join(c.DataDir, "sdk-scenes.mp4"), filepath.Join(c.DataDir, "sdk-replacement.wav")
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "color=c=red:s=160x96:r=25:d=1[red];color=c=black:s=160x96:r=25:d=1[black];color=c=blue:s=160x96:r=25:d=2[blue];[red][black][blue]concat=n=3:v=1:a=0", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-af", "volume=0.1,volume=0:enable='between(t,1,2)'", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", input)
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=1", "-c:a", "pcm_s16le", audio)
	return input, audio
}

func checkSDKFeatures(t *testing.T, ctx context.Context, c Config, r sdkFeatureReport) {
	t.Helper()
	s, v, l := r.Silence, r.Video, r.Loudness
	near := func(a, b int64) bool { return a >= b-100 && a <= b+100 }
	if s.Version != 1 || s.AudioTrack != 1 || !near(s.EndMS, 4000) || s.AnalyzedEndMS < 3950 || len(s.Silence) != 1 || !near(s.Silence[0].StartMS, 1000) || !near(s.Silence[0].EndMS, 2000) || len(s.Keep) != 2 {
		t.Fatalf("silence differs from independently known source clock: %+v", s)
	}
	if v.Version != 1 || v.VideoTrack != 0 || v.AnalyzedEndMS < 3900 || len(v.Black) != 1 || !near(v.Black[0].StartMS, 1000) || !near(v.Black[0].EndMS, 2000) || len(v.Scenes) < 1 {
		t.Fatalf("video differs from known black/scene boundaries: %+v", v)
	}
	if l.IntegratedLUFS == nil || l.TruePeakDBTP == nil || l.LoudnessRangeLU == nil || l.AnalyzedEndMS < 3950 || *l.IntegratedLUFS > -30 {
		t.Fatalf("unexpected quiet-source loudness: %+v", l)
	}
	if len(r.Outputs) != 3 {
		t.Fatalf("missing render feature outputs: %+v", r.Outputs)
	}
	names := map[string]bool{}
	for _, out := range r.Outputs {
		if names[out.Name] {
			t.Fatal("duplicate feature output")
		}
		names[out.Name] = true
		streams := mcProbe(t, ctx, c, out.Path)
		_, duration, err := probe(ctx, c, out.Path, false)
		file, statErr := os.Stat(out.Path)
		if err != nil || !near(duration, 4000) || statErr != nil || file.Size() != out.Artifact.SizeBytes || out.Artifact.Segments == nil || len(*out.Artifact.Segments) != 1 || out.Artifact.TrackManifest == nil {
			t.Fatalf("feature output lost duration/size/manifests: %+v duration=%d err=%v/%v", out, duration, err, statErr)
		}
		segment := (*out.Artifact.Segments)[0]
		if segment.SourceStartMS != 0 || !near(segment.SourceEndMS, 4000) || segment.OutputStartMS != 0 || !near(segment.OutputEndMS, 4000) {
			t.Fatalf("feature output changed source/output clock: %+v", segment)
		}
		mcRun(t, ctx, c, "-i", out.Path, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
		switch out.Name {
		case "normalize":
			mcSquareGeometry(t, ctx, c, out.Path, 160, 96)
			measured, e := engine.New(engine.Config{FFmpegPath: c.FFmpeg, FFprobePath: c.FFprobe, FFmpegThreads: 1}).AnalyzeLoudness(ctx, out.Path, engine.LoudnessOptions{})
			if e != nil || measured.IntegratedLUFS == nil || math.Abs(*measured.IntegratedLUFS+16) > 1.5 {
				t.Fatalf("normalization did not reach requested LUFS: %+v %v", measured, e)
			}
		case "canvas":
			mcSquareGeometry(t, ctx, c, out.Path, 96, 160)
			rgb, w, h := mcFrame(t, ctx, c, out.Path, false)
			corner, center := rgb[:3], rgb[((h/2)*w+w/2)*3:][:3]
			if corner[2] < 180 || corner[0] > 60 || center[0] < 180 || center[2] > 60 {
				t.Fatalf("canvas background/content pixels wrong: %v %v", corner, center)
			}
		case "replace":
			mcSquareGeometry(t, ctx, c, out.Path, 160, 96)
			index := mcAudioIndex(t, streams)
			mcTone(t, mcPCM(t, ctx, c, out.Path, 1.2, .2, index), 880)
			for _, start := range []float64{.2, 2.5, 3.5} {
				samples := mcPCM(t, ctx, c, out.Path, start, .2, index)
				var power float64
				for _, x := range samples {
					power += float64(x) * float64(x)
				}
				if math.Sqrt(power/float64(len(samples))) > 15 {
					t.Fatalf("replacement failed to pad silence at %v", start)
				}
			}
		default:
			t.Fatalf("unknown rendered feature: %s", out.Name)
		}
	}
	for _, name := range []string{"normalize", "canvas", "replace"} {
		if !names[name] {
			t.Fatal("missing feature", name)
		}
	}
}

func runSDKFeatures(t *testing.T, ctx context.Context, consumer sdkConsumer, mode, input, audio, output, baseURL string) sdkFeatureReport {
	t.Helper()
	extension := ".py"
	if consumer.name == "typescript" {
		extension = ".mjs"
	}
	script := filepath.Join(filepath.Dir(consumer.script), "sdk-media-acceptance"+extension)
	command := exec.CommandContext(ctx, consumer.runtime, script, mode, input, audio, output, baseURL)
	command.Env = os.Environ()
	stdout, stderr := &sdkOutputBuffer{limit: 1 << 20}, &sdkOutputBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		t.Fatalf("installed %s %s media features: %v\n%s", consumer.name, mode, err, stderr.buffer.String())
	}
	var report sdkFeatureReport
	if err := json.Unmarshal(stdout.buffer.Bytes(), &report); err != nil || report.Mode != mode {
		t.Fatalf("invalid feature receipt: %v %s", err, stdout.buffer.String())
	}
	return report
}

func TestInstalledSDKMediaLocalWorkflow(t *testing.T) {
	for _, consumer := range installedSDKConsumers(t, false) {
		t.Run(consumer.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			c := mediaConfig(t)
			input, audio := sdkFeatureFixture(t, ctx, c)
			checkSDKFeatures(t, ctx, c, runSDKFeatures(t, ctx, consumer, "local", input, audio, t.TempDir(), ""))
		})
	}
}

func TestInstalledSDKMediaHTTPWorkflow(t *testing.T) {
	for _, consumer := range installedSDKConsumers(t, true) {
		t.Run(consumer.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			store := testStore(t)
			c := mediaConfig(t)
			c.PublicOrigin = "https://sdk-public.example"
			c.WorkerHealthPath = filepath.Join(t.TempDir(), "worker-health")
			c.WorkerConcurrency, c.FFmpegThreads = 1, 1
			c.SourceTimeout, c.UploadTimeout, c.JobTimeout = 30*time.Second, 30*time.Second, 45*time.Second
			c.SourceTTL, c.ArtifactTTL, c.StorageWaitTimeout = time.Hour, time.Hour, time.Minute
			c.MaxStorageBytes, c.MaxOwnerBytes, c.MaxActiveJobs, c.MutationsPerMinute = 1<<30, 1<<30, 32, 100
			input, audio := sdkFeatureFixture(t, ctx, c)
			server := httptest.NewServer(NewServer(c, store).Handler())
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
			checkSDKFeatures(t, ctx, c, runSDKFeatures(t, ctx, consumer, "http", input, audio, t.TempDir(), server.URL))
		})
	}
}
