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

func cliCollectionFixture(t *testing.T) (string, string) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if blob, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "3", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", input).CombinedOutput(); err != nil {
		t.Fatalf("fixture %v %s", err, blob)
	}
	path := filepath.Join(dir, "collection.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"input":"source.mp4","options":{"format":"mp4"},"clips":[{"id":"highlight","label":"Later then earlier","ranges":[{"start_ms":1500,"end_ms":2200},{"start_ms":200,"end_ms":900}]},{"id":"other","ranges":[{"start_ms":1000,"end_ms":1700}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return input, path
}

func TestCLICollectionWorkflow(t *testing.T) {
	_, plan := cliCollectionFixture(t)
	dir := t.TempDir()
	var out, diagnostics bytes.Buffer
	args := []string{"--json-errors", "collection", "validate", "--plan", plan, "--output", dir, "--threads", "1"}
	if code := runCLI(args, &out, &diagnostics); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("validate %d %s", code, diagnostics.String())
	}
	var checked engine.CollectionPreflightResult
	if err := json.Unmarshal(out.Bytes(), &checked); err != nil || len(checked.Clips) != 2 || checked.Clips[0].RequestedMS != 1400 {
		t.Fatalf("report %+v %v", checked, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("validation wrote files")
	}
	out.Reset()
	diagnostics.Reset()
	args = []string{"--json-errors", "collection", "export", "--plan", plan, "--output", dir, "--rendition", "draft", "--threads", "1"}
	if code := runCLI(args, &out, &diagnostics); code != 0 {
		t.Fatalf("draft %d %s", code, diagnostics.String())
	}
	var draft engine.CollectionResult
	if err := json.Unmarshal(out.Bytes(), &draft); err != nil || draft.Rendition != "draft" || len(draft.Outputs) != 2 || filepath.Base(draft.Outputs[0].Path) != "highlight.draft.mp4" || len(draft.Outputs[0].Segments) != 2 {
		t.Fatalf("draft %+v %v", draft, err)
	}
	out.Reset()
	diagnostics.Reset()
	args = []string{"--json-errors", "collection", "export", "--plan", plan, "--output", dir, "--clips", "highlight", "--threads", "1"}
	if code := runCLI(args, &out, &diagnostics); code != 0 {
		t.Fatalf("selected %d %s", code, diagnostics.String())
	}
	var selected engine.CollectionResult
	if err := json.Unmarshal(out.Bytes(), &selected); err != nil || len(selected.Outputs) != 1 || selected.Outputs[0].ID != "highlight" || filepath.Base(selected.Outputs[0].Path) != "highlight.final.mp4" {
		t.Fatalf("selected %+v %v", selected, err)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("unexpected selection %+v", entries)
	}
	prior, _ := os.ReadFile(selected.Outputs[0].Path)
	out.Reset()
	diagnostics.Reset()
	if code := runCLI(args, &out, &diagnostics); code != 1 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"output_exists"`) || !strings.Contains(diagnostics.String(), `"clip_id":"highlight"`) {
		t.Fatalf("collision %d %s %s", code, out.String(), diagnostics.String())
	}
	after, _ := os.ReadFile(selected.Outputs[0].Path)
	if !bytes.Equal(prior, after) {
		t.Fatal("collision altered result")
	}
}

func TestCLICollectionStrictPlansAndDiagnostics(t *testing.T) {
	input, _ := cliCollectionFixture(t)
	for _, text := range []string{
		`{"version":1,"input":"source.mp4","clps":[]}`,
		`{"version":1} {"version":1}`,
		strings.Repeat(" ", (1<<20)+1),
		`null`,
	} {
		path := filepath.Join(t.TempDir(), "collection.json")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		var out, diagnostics bytes.Buffer
		if code := runCLI([]string{"--json-errors", "collection", "validate", "--plan", path, "--output", t.TempDir()}, &out, &diagnostics); code != 2 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"invalid_argument"`) {
			t.Fatalf("strict %d %s %s", code, out.String(), diagnostics.String())
		}
	}
	plan := engine.CollectionPlan{Version: 1, Input: input, Clips: []engine.CollectionClip{{ID: "highlight", Ranges: []engine.CollectionRange{{StartMS: 0, EndMS: 9000}}}}}
	blob, _ := json.Marshal(plan)
	path := filepath.Join(t.TempDir(), "collection.json")
	_ = os.WriteFile(path, blob, 0600)
	var out, diagnostics bytes.Buffer
	if code := runCLI([]string{"--json-errors", "collection", "validate", "--plan", path, "--output", t.TempDir()}, &out, &diagnostics); code != 2 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"clip_id":"highlight"`) || !strings.Contains(diagnostics.String(), `"field":"ranges[0].end_ms"`) {
		t.Fatalf("location %d %s %s", code, out.String(), diagnostics.String())
	}
	for _, args := range [][]string{{"collection", "unknown"}, {"collection", "validate"}, {"collection", "export", "--plan", path, "--output", t.TempDir(), "--clips", "highlight,"}, {"collection", "export", "--plan", path, "--output", t.TempDir(), "--max-output-bytes", "0"}} {
		out.Reset()
		diagnostics.Reset()
		if code := runCLI(append([]string{"--json-errors"}, args...), &out, &diagnostics); code != 2 {
			t.Fatalf("args %v code=%d %s", args, code, diagnostics.String())
		}
	}
}
