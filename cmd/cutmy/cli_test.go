package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTimestampsRejectAmbiguousComponents(t *testing.T) {
	for input, want := range map[string]int64{"0": 0, "90.125": 90125, "01:30.125": 90125, "02:01:30.125": 7290125, "0.0005": 1} {
		got, err := parseTime(input)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "-1", "+1", "1e3", "1.5:00", "1:2.5:00", "1:60", "1:00:60", "1::2", "1:2:3:4", ".5", "1.", "NaN", "Inf", "2592001", " 1"} {
		if got, err := parseTime(input); err == nil {
			t.Errorf("accepted ambiguous/out-of-bounds timestamp %q: %d", input, got)
		}
	}
}

func TestCLIInfoDoesNotRequireServerConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for _, args := range [][]string{nil, {"--help"}, {"clip", "--help"}, {"inspect", "--help"}, {"server", "--help"}, {"maintenance", "--help"}} {
		var stdout, stderr bytes.Buffer
		if err := runArgs(args, &stdout, &stderr); err != nil || stdout.Len()+stderr.Len() == 0 {
			t.Errorf("help %v: %v", args, err)
		}
	}
	var stdout bytes.Buffer
	if err := runArgs([]string{"version"}, &stdout, &stdout); err != nil {
		t.Fatal(err)
	}
	var info map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil || info["version"] == "" || info["go_version"] == "" || info["os"] == "" || info["arch"] == "" {
		t.Fatalf("build information: %s, %v", &stdout, err)
	}
	for _, args := range [][]string{{"unknown"}, {"version", "extra"}, {"server", "extra"}, {"clip", "extra"}, {"inspect", "--input", "absent", "extra"}, {"inspect", "--input", "absent", "--timeout", "0"}} {
		if err := runArgs(args, &stdout, &stdout); err == nil {
			t.Errorf("accepted invalid arguments: %v", args)
		}
	}
}

func TestCLIInspectAndClipActualMedia(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is not installed")
		}
	}
	t.Setenv("DATABASE_URL", "")
	input := filepath.Join(t.TempDir(), "-recording.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-t", "2", "-c:v", "libx264", "-threads", "1", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	var stdout, stderr bytes.Buffer
	if err := runArgs([]string{"inspect", "--input", input}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var inspected struct {
		Duration int64 `json:"duration_ms"`
		Width    int   `json:"width"`
		Height   int   `json:"height"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &inspected); err != nil || inspected.Duration != 2000 || inspected.Width != 160 || inspected.Height != 90 {
		t.Fatalf("inspection: %s, %v", &stdout, err)
	}
	stdout.Reset()
	output := filepath.Join(t.TempDir(), "moment.mp4")
	args := []string{"clip", "--input", input, "--output", output, "--start", "0.5", "--end", "1.5", "--timeout", "15s"}
	if err := runArgs(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), `"actual_start_ms":500`) {
		t.Fatalf("export: %s", &stdout)
	}
	if err := runArgs(args, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output was not protected: %v", err)
	}
}
