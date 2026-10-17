package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func interfacesCLI(t *testing.T, args ...string) []byte {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if code := runCLI(append([]string{"--json-errors"}, args...), &out, &diagnostics); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("%v: code=%d %s", args, code, diagnostics.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("non-JSON successful output: %s", out.String())
	}
	return out.Bytes()
}

func TestNewInterfacesCLIActualAnalysisAndExplicitTransforms(t *testing.T) {
	input, _ := cliCollectionFixture(t)
	t.Setenv("DATABASE_URL", "")
	var silence engine.SilenceResult
	if err := json.Unmarshal(interfacesCLI(t, "analyze", "silence", "--input", input, "--start", "0.5", "--end", "2.5"), &silence); err != nil || silence.Version != 1 || silence.StartMS != 500 || silence.AnalyzedEndMS < 2490 || len(silence.Keep) != 1 {
		t.Fatal(silence, err)
	}
	var plan engine.Plan
	if err := json.Unmarshal(interfacesCLI(t, "analyze", "silence", "--input", input, "--end", "2", "--plan"), &plan); err != nil || plan.Version != 1 || plan.Input != input || len(plan.Ranges) != 1 || plan.Options.AudioTrack == nil {
		t.Fatal(plan, err)
	}
	var video engine.VideoAnalysisResult
	if err := json.Unmarshal(interfacesCLI(t, "analyze", "video", "--input", input, "--end", "2"), &video); err != nil || video.Version != 1 || video.AnalyzedEndMS <= 1900 || video.AnalyzedEndMS > 2000 || video.Scenes == nil || video.Black == nil {
		t.Fatal(video, err)
	}
	var loudness engine.LoudnessResult
	if err := json.Unmarshal(interfacesCLI(t, "analyze", "loudness", "--input", input, "--end", "2"), &loudness); err != nil || loudness.Version != 1 || loudness.IntegratedLUFS == nil || loudness.TruePeakDBTP == nil {
		t.Fatal(loudness, err)
	}
	output := filepath.Join(t.TempDir(), "transformed.mp4")
	interfacesCLI(t, "clip", "--input", input, "--output", output, "--end", "2", "--threads", "1", "--canvas", "192x192", "--audio-replace", input, "--audio-offset-ms", "100", "--audio-normalize")
	var inspected engine.SourceInfo
	if err := json.Unmarshal(interfacesCLI(t, "inspect", "--input", output), &inspected); err != nil || inspected.Width != 192 || inspected.Height != 192 || inspected.DurationMS < 1950 {
		t.Fatal(inspected, err)
	}
}

func TestNewInterfacesCLICutListRoundTripAndActualOutputClock(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "markers.csv")
	if err := os.WriteFile(csv, []byte("0,,Привет\n1,,Second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data := interfacesCLI(t, "cutlist", "import", "--format", "csv", "--input", csv)
	var list engine.CutList
	if err := json.Unmarshal(data, &list); err != nil || len(list.Entries) != 2 || list.Entries[0].EndMS != nil || list.Entries[0].Label != "Привет" {
		t.Fatal(list, err)
	}
	var plan engine.Plan
	if err := json.Unmarshal(interfacesCLI(t, "cutlist", "import", "--format", "csv", "--input", csv, "--plan", "--media", "not-opened.mp4", "--duration-ms", "3000"), &plan); err != nil || !filepath.IsAbs(plan.Input) || filepath.Base(plan.Input) != "not-opened.mp4" || plan.Ranges[0].EndMS != 1000 || plan.Ranges[1].EndMS != 3000 {
		t.Fatal(plan, err)
	}
	planPath := filepath.Join(dir, "plan.json")
	blob, _ := json.Marshal(plan)
	if err := os.WriteFile(planPath, blob, 0600); err != nil {
		t.Fatal(err)
	}
	var text struct {
		Version      int
		Format, Text string
	}
	if err := json.Unmarshal(interfacesCLI(t, "cutlist", "export", "--format", "ffmetadata", "--plan", planPath), &text); err != nil || text.Version != 1 || !strings.Contains(text.Text, "START=1000\nEND=3000") || !strings.Contains(text.Text, "Привет") {
		t.Fatal(text, err)
	}
	segments := filepath.Join(dir, "segments.json")
	if err := os.WriteFile(segments, []byte(`[{"index":0,"label":"actual","source_start_ms":9000,"source_end_ms":10000,"output_start_ms":0,"output_end_ms":1100}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(interfacesCLI(t, "cutlist", "export", "--format", "csv", "--segments", segments), &text); err != nil || text.Text != "0,1.100,actual\n" {
		t.Fatal(text, err)
	}
	listPath := filepath.Join(dir, "list.json")
	if err := os.WriteFile(listPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(interfacesCLI(t, "cutlist", "export", "--format", "csv", "--input", listPath), &text); err != nil || text.Text != "0,,Привет\n1,,Second\n" {
		t.Fatal(text, err)
	}
}

func TestNewInterfacesCLIStrictFlagsAndJSONErrors(t *testing.T) {
	for _, args := range [][]string{
		{"analyze", "unknown"}, {"analyze", "silence"}, {"analyze", "video", "--input", "x", "--audio-track", "0"},
		{"analyze", "silence", "--input", "x", "--audio-track", "none"}, {"analyze", "silence", "--input", "x", "--format", "mp3"},
		{"analyze", "video", "--input", "x", "--start", "2", "--end", "1"}, {"analyze", "loudness", "--input", "x", "--timeout", "0s"},
		{"cutlist", "export", "--format", "csv", "--input", "x", "--plan", "y"}, {"cutlist", "import", "--format", "chapters", "--input", "x"},
		{"cutlist", "import", "--format", "csv", "--input", "x", "--media", "m"}, {"cutlist", "import", "--format", "csv", "--input", "x", "--plan", "--media", "m"},
		{"clip", "--input", "x", "--output", "x.mp4", "--end", "1", "--normalize-lufs", "-16"},
		{"clip", "--input", "x", "--output", "x.mp4", "--end", "1", "--canvas-background", "#000000"},
		{"clip", "--input", "x", "--output", "x.mp4", "--end", "1", "--audio-offset-ms", "100"},
		{"clip", "--input", "x", "--output", "x.mp4", "--end", "1", "--canvas", "192"},
	} {
		var out, diagnostics bytes.Buffer
		if code := runCLI(append([]string{"--json-errors"}, args...), &out, &diagnostics); code != 2 || out.Len() != 0 || !json.Valid(diagnostics.Bytes()) || !strings.Contains(diagnostics.String(), `"code":"invalid_argument"`) {
			t.Fatalf("%v code=%d %s %s", args, code, out.String(), diagnostics.String())
		}
	}
	for _, command := range []string{"analyze", "cutlist"} {
		interfacesCLIHelp(t, command)
	}
}

func interfacesCLIHelp(t *testing.T, command string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if code := runCLI([]string{command, "--help"}, &out, &diagnostics); code != 0 || out.Len() == 0 {
		t.Fatal(command, code, diagnostics.String())
	}
}

func TestNewInterfacesCLIRelativeReplacementInputsAndLocalIsolation(t *testing.T) {
	dir := t.TempDir()
	batch := filepath.Join(dir, "batch.json")
	collection := filepath.Join(dir, "collection.json")
	if err := os.WriteFile(batch, []byte(`{"version":1,"input":"source.mp4","ranges":[{"start_ms":0,"end_ms":1000}],"options":{"audio_replace":{"input":"audio.wav"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := readCuttingPlan(batch)
	if err != nil || plan.Options.AudioReplace.Input != filepath.Join(dir, "audio.wav") || plan.Input != filepath.Join(dir, "source.mp4") {
		t.Fatal(plan, err)
	}
	if err := os.WriteFile(collection, []byte(`{"version":1,"input":"source.mp4","options":{"audio_replace":{"input":"default.wav"}},"clips":[{"id":"one","ranges":[{"start_ms":0,"end_ms":1000}],"options":{"audio_replace":{"input":"clip.wav"}}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	list, err := readCollectionPlan(collection)
	if err != nil || list.Options.AudioReplace.Input != filepath.Join(dir, "default.wav") || list.Clips[0].Options.AudioReplace.Input != filepath.Join(dir, "clip.wav") {
		t.Fatal(list, err)
	}
	for _, path := range []string{batch, collection} {
		if err := os.WriteFile(path, []byte(`{"version":1,"input":"source.mp4","options":{"audio_replace":{"source_id":"private-source"}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if path == batch {
			_, err = readCuttingPlan(path)
		} else {
			_, err = readCollectionPlan(path)
		}
		if engine.CodeOf(err) != engine.CodeInvalidArgument {
			t.Fatal("local source_id accepted", err)
		}
	}
}
