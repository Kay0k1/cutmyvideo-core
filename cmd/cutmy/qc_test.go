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

func qcFixture(t *testing.T, name string, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "ffmpeg", append(append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...), path)...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	return path
}

func TestQCCLIReportsAndExitCodes(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is not installed")
		}
	}
	video := qcFixture(t, "-video.mp4", "-f", "lavfi", "-i", "testsrc2=s=160x120:r=25:d=2", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-movflags", "+faststart")
	audio := qcFixture(t, "-audio.m4a", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:d=2", "-c:a", "aac")

	type report struct {
		Version int
		Passed  bool
		Video   *struct {
			FrameCount int `json:"frame_count"`
			Timing     string
		}
		Audio  *struct{ Channels int }
		Sync   *struct{}
		Checks []struct{ Name, Status, Message string }
	}
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"--json-errors", "qc", "--input", video, "--expect", "fps=25,size=160x120", "--expect", "cfr,no-drops,faststart,no-audio"}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("passing video: exit %d, %s %s", code, &stdout, &stderr)
	}
	var r report
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil || r.Version != 1 || !r.Passed || len(r.Checks) != 6 || r.Video == nil || r.Video.FrameCount != 50 || r.Video.Timing != "cfr" || r.Audio != nil || r.Sync != nil {
		t.Fatalf("video-only report %s: %v", &stdout, err)
	}

	// A failed expectation still prints the whole report, then exits 1.
	stdout.Reset()
	if code := runCLI([]string{"--json-errors", "qc", "--input", audio, "--expect", "audio,video,true-peak=-30,no-clipping"}, &stdout, &stderr); code != 1 {
		t.Fatalf("failing audio: exit %d, %s", code, &stderr)
	}
	r = report{}
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil || r.Passed || r.Video != nil || r.Audio == nil || r.Audio.Channels != 1 || r.Sync != nil || len(r.Checks) != 4 || r.Checks[1].Status != "fail" || r.Checks[1].Message == "" || r.Checks[2].Status != "fail" || r.Checks[3].Status != "pass" {
		t.Fatalf("audio-only report %s: %v", &stdout, err)
	}
	if !strings.Contains(stderr.String(), `"code":"expectation_failed"`) {
		t.Fatalf("stderr %s", &stderr)
	}

	// Measurement alone exits 0.
	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"qc", "--input", audio}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"passed":true`) {
		t.Fatalf("measure only: exit %d, %s %s", code, &stdout, &stderr)
	}
}

func TestQCCLIRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"qc", "--expect", "cfr"},
		{"qc", "--input", "final.mp4", "extra"},
		{"qc", "--input", "final.mp4", "--expect", "fps"},
		{"qc", "--input", "final.mp4", "--expect", "fps=sixty"},
		{"qc", "--input", "final.mp4", "--expect", "bitrate=1"},
		{"qc", "--input", "final.mp4", "--expect", "cfr,,no-drops"},
		{"qc", "--input", "final.mp4", "--threads", "0"},
		{"qc", "--input", "final.mp4", "--timeout", "0"},
		{"qc", "--input", "final.mp4", "--start", "5", "--end", "5"},
		{"qc", "--input", "final.mp4", "--min-freeze-ms", "10"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runCLI(append([]string{"--json-errors"}, args...), &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"invalid_argument"`) {
			t.Errorf("%v: exit %d, stdout %s, stderr %s", args, code, &stdout, &stderr)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := runArgs([]string{"qc", "--help"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "expectation_failed") {
		t.Fatalf("help: %v %s", err, &stdout)
	}
}
