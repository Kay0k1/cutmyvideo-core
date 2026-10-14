package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func TestCLIPreflightPlanJSONAndNoOutput(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "input.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=2:size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", input)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, data)
	}
	plan := filepath.Join(dir, "cuts.json")
	if err := os.WriteFile(plan, []byte(`{"version":1,"input":"input.mp4","ranges":[{"label":"Later","start_ms":1000,"end_ms":1500},{"label":"Earlier","start_ms":0,"end_ms":500}],"output_mode":"joined","options":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "joined.mp4")
	var out, diagnostics bytes.Buffer
	if code := runCLI([]string{"--json-errors", "preflight", "--plan", plan, "--output", output}, &out, &diagnostics); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, &diagnostics)
	}
	var checked engine.PreflightResult
	if err := json.Unmarshal(out.Bytes(), &checked); err != nil {
		t.Fatal(err)
	}
	if !checked.Valid || checked.Version != 1 || checked.OutputCount != 1 || checked.RequestedMS != 1000 || len(checked.OutputPaths) != 1 || checked.OutputPaths[0] != output || checked.Request.Format != "mp4" || checked.Request.CutMode != "accurate" || len(checked.TrackManifest) != 2 || len(checked.Warnings) < 4 {
		t.Fatalf("report: %s", &out)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("preflight created files: %v %v", files, err)
	}
	out.Reset()
	if code := runCLI([]string{"batch", "--plan", plan, "--output", output}, &out, &diagnostics); code != 0 {
		t.Fatalf("checked plan failed: %d %s", code, &diagnostics)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal("checked plan did not produce predicted output", err)
	}
}

func TestCLIPreflightStrictPlansAndDiagnostics(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"input":"file.mp4","options":{"quailty":"best"}}`,
		`{"version":1} {"version":1}`,
		`null`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		path := filepath.Join(t.TempDir(), "cuts.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		assertPreflightCLIError(t, []string{"--json-errors", "preflight", "--plan", path, "--output", t.TempDir()}, 2, engine.CodeInvalidArgument)
	}
	for _, args := range [][]string{
		{"preflight"}, {"preflight", "--unknown"}, {"preflight", "--plan", "unused", "--output", "unused", "extra"},
		{"preflight", "--plan", "unused", "--output", "unused", "--timeout", "0s"},
		{"preflight", "--plan", "unused", "--output", "unused", "--threads", "33"},
	} {
		assertPreflightCLIError(t, append([]string{"--json-errors"}, args...), 2, engine.CodeInvalidArgument)
	}
	assertPreflightCLIError(t, []string{"--json-errors", "preflight", "--plan", filepath.Join(t.TempDir(), "absent"), "--output", t.TempDir()}, 1, engine.CodeInputNotFound)
	var out, diagnostics bytes.Buffer
	if code := runCLI([]string{"preflight", "--help"}, &out, &diagnostics); code != 0 || out.Len()+diagnostics.Len() == 0 {
		t.Fatal("preflight help failed")
	}
}

func assertPreflightCLIError(t *testing.T, args []string, status int, code engine.ErrorCode) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if got := runCLI(args, &out, &diagnostics); got != status || out.Len() != 0 {
		t.Fatalf("status=%d stdout=%s stderr=%s", got, &out, &diagnostics)
	}
	var response struct {
		Error struct {
			Code    engine.ErrorCode
			Message string
		}
	}
	if err := json.Unmarshal(diagnostics.Bytes(), &response); err != nil || response.Error.Code != code || response.Error.Message == "" {
		t.Fatalf("diagnostics: %s %v", &diagnostics, err)
	}
}
