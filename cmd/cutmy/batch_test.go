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
)

func TestBatchRejectsAmbiguousOrMisspelledPlans(t *testing.T) {
	for _, plan := range []string{
		`{"version":1,"input":"file.mp4","output_mode":"joined","outpt_mode":"separate"}`,
		`{"version":1} {"version":1}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		t.Run(plan[:min(len(plan), 25)], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cuts.json")
			if err := os.WriteFile(path, []byte(plan), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			code := runCLI([]string{"--json-errors", "batch", "--plan", path, "--output", t.TempDir()}, &out, &diagnostics)
			if code != 2 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"invalid_argument"`) {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), diagnostics.String())
			}
		})
	}
}

func TestCLIBatchSavedPlanRelativeInputAndJoinedAudio(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is not installed")
		}
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "input.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=2:size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-f", "lavfi", "-i", "sine=frequency=880:duration=2", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	plan := filepath.Join(dir, "cuts.json")
	if err := os.WriteFile(plan, []byte(`{"version":1,"input":"input.mp4","ranges":[{"label":"Later","start_ms":1000,"end_ms":1500},{"label":"Earlier","start_ms":0,"end_ms":500}],"output_mode":"joined","options":{"format":"mp4","audio_track":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "joined.mp4")
	var out, diagnostics bytes.Buffer
	if code := runCLI([]string{"batch", "--plan", plan, "--output", output}, &out, &diagnostics); code != 0 {
		t.Fatalf("code=%d %s", code, diagnostics.String())
	}
	var result struct {
		Outputs []struct {
			Path     string
			Segments []struct {
				Label         string
				OutputStartMS int64 `json:"output_start_ms"`
			}
		}
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Path != output || len(result.Outputs[0].Segments) != 2 || result.Outputs[0].Segments[0].Label != "Later" || result.Outputs[0].Segments[1].OutputStartMS != 500 {
		t.Fatalf("result: %s", out.String())
	}
	cmd = exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", output, "-f", "null", "-")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("independent decode: %v %s", err, raw)
	}
}

func TestAudioSelectionAcceptsInspectedIndices(t *testing.T) {
	for _, value := range []string{"-1", "English", "1.0", "2147483648"} {
		if _, _, err := parseAudioSelection(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	index, noAudio, err := parseAudioSelection("2")
	if err != nil || index == nil || *index != 2 || noAudio {
		t.Fatalf("index selection: %v %v %v", index, noAudio, err)
	}
	index, noAudio, err = parseAudioSelection("none")
	if err != nil || index != nil || !noAudio {
		t.Fatalf("silent selection: %v %v %v", index, noAudio, err)
	}
}
