package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is not installed")
		}
	}
	input := filepath.Join(t.TempDir(), "-source.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-threads", "1", "-g", "25", "-sc_threshold", "0", "-c:a", "aac", "-movflags", "+faststart", input).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture: %v %s", err, data)
	}
	return input
}

func TestEngineInspectAndExportActualMedia(t *testing.T) {
	input := fixture(t)
	e := New(Config{MaxOutputBytes: 10 << 20, FFmpegThreads: 1})
	info, err := e.Inspect(context.Background(), input)
	if err != nil || info.DurationMS < 3900 || info.Width != 160 || info.Height != 90 {
		t.Fatalf("inspect: %+v %v", info, err)
	}
	before, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ format, mode string }{{"mp4", "accurate"}, {"mp3", "accurate"}, {"mp4", "copy"}} {
		t.Run(tc.format+"-"+tc.mode, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "result."+tc.format)
			result, err := e.Export(context.Background(), input, output, Range{StartMS: 1200, EndMS: 3000}, Options{Format: tc.format, CutMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			if result.Path != output || result.SizeBytes <= 0 || result.ActualEndMS <= result.ActualStartMS {
				t.Fatalf("invalid result: %+v", result)
			}
			if tc.mode == "accurate" && (result.ActualStartMS != 1200 || result.ActualEndMS < 2900 || result.ActualEndMS > 3350) {
				t.Fatalf("inaccurate result: %+v", result)
			}
			if tc.mode == "copy" && result.ActualStartMS > 1200 {
				t.Fatalf("copy skipped requested start: %+v", result)
			}
			after, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.Export(context.Background(), input, output, Range{StartMS: 0, EndMS: 1000}, Options{}); !errors.Is(err, ErrOutputExists) {
				t.Fatal("existing output not reported correctly", err)
			}
			unchanged, _ := os.ReadFile(output)
			if string(unchanged) != string(after) {
				t.Fatal("existing output changed")
			}
		})
	}
	after, _ := os.ReadFile(input)
	if string(before) != string(after) {
		t.Fatal("input changed")
	}
}

func TestEngineInvalidLimitsAndCancellationLeaveNoOutput(t *testing.T) {
	for _, cfg := range []Config{{MaxOutputBytes: -1}, {FFmpegThreads: 33}, {FFmpegThreads: -1}, {EncodeProfile: "invalid"}, {InspectTimeout: -time.Second}, {ExportTimeout: -time.Second}} {
		if _, err := New(cfg).Inspect(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), "invalid engine") {
			t.Fatal("invalid config accepted", cfg, err)
		}
	}
	input := fixture(t)
	output := filepath.Join(t.TempDir(), "result.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Config{}).Export(ctx, input, output, Range{EndMS: 1000}, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, err := New(Config{InspectTimeout: time.Nanosecond}).Inspect(context.Background(), input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("inspect deadline lost", err)
	}
	if _, err := New(Config{ExportTimeout: time.Nanosecond}).Export(context.Background(), input, output, Range{EndMS: 1000}, Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("export deadline lost", err)
	}
	files, err := os.ReadDir(filepath.Dir(output))
	if err != nil || len(files) != 0 {
		t.Fatal("failed operation leaked files", files, err)
	}
	if _, err := New(Config{MaxOutputBytes: 1024}).Export(context.Background(), input, output, Range{EndMS: 3000}, Options{}); err == nil {
		t.Fatal("output bound ignored")
	}
	files, err = os.ReadDir(filepath.Dir(output))
	if err != nil || len(files) != 0 {
		t.Fatal("oversized operation leaked files", files, err)
	}
}

func TestEngineOutputPublicationIsExclusiveIncludingDanglingSymlink(t *testing.T) {
	input := fixture(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "result.mp4")
	e := New(Config{FFmpegThreads: 1})
	if err := os.Symlink(filepath.Join(dir, "absent"), output); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Export(context.Background(), input, output, Range{EndMS: 1000}, Options{}); !errors.Is(err, ErrOutputExists) {
		t.Fatal("existing symlink not reported correctly", err)
	}
	if _, err := os.Lstat(output); err != nil {
		t.Fatal("deleted preexisting output")
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Export(context.Background(), input, output, Range{EndMS: 1000}, Options{})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrOutputExists) {
			t.Fatal("concurrent existing output not reported correctly", err)
		}
	}
	if successes != 1 {
		t.Fatal("exclusive publication failed", successes)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 || files[0].Name() != "result.mp4" {
		t.Fatal("temporary files leaked", files)
	}
}
