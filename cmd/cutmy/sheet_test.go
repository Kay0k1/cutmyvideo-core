package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func TestSheetCLIStrictFlagsAndJSONErrors(t *testing.T) {
	base := []string{"sheet", "--input", "a.mp4", "--output", "s.jpg"}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
	for _, args := range [][]string{
		{"sheet"}, {"sheet", "--input", "a.mp4"}, {"sheet", "--output", "s.jpg"}, with("extra"), with("--unknown"),
		// Exactly one kind of source.
		with("--images", "frames"), with("--compare", "a.mp4", "b.mp4"),
		{"sheet", "--compare", "a.mp4", "--output", "s.jpg"}, {"sheet", "--compare", "a.mp4", "b.mp4", "c.mp4", "--output", "s.jpg"},
		{"sheet", "--images", "frames", "--compare", "a.mp4", "b.mp4", "--output", "s.jpg"},
		// Exactly one way of choosing frames.
		with("--every", "2s", "--count", "4"), with("--at", "1,2", "--scenes"), with("--count", "4", "--at", "1"), with("--scenes", "--every", "1"),
		with("--scenes", "analysis.json", "--count", "4"), with("--every", "2s", "--count", "4", "--at", "1", "--scenes"),
		with("--count", "0"), with("--count", "-3"), with("--count", "many"),
		with("--every", "0"), with("--every", "-2s"), with("--every", "fast"), with("--every", "100us"), with("--every", "1500us"), with("--every", ""),
		with("--at", "1,,2"), with("--at", "1e3"), with("--at", ""), with("--at", "-1"),
		with("--range", "5"), with("--range", "5-2"), with("--range", "-"), with("--range", "1-2-3"), with("--range", "a-b"),
		with("--label", "date"), with("--label", "none,time"), with("--label", "time,time"), with("--label", ""),
		with("--format", "webp"), with("--format", "png"), {"sheet", "--input", "a.mp4", "--output", "s.gif"},
		with("--threads", "0"), with("--threads", "33"), with("--threads", "many"), with("--timeout", "0s"),
		with("--cols", "0"), with("--cols", "65"), with("--max-rows", "0"), with("--tile-width", "10"), with("--tile-width", "4000"),
		with("--ssim-threshold", "0.9"),
		{"sheet", "--compare", "a.mp4", "b.mp4", "--output", "s.jpg", "--cols", "3"},
		{"sheet", "--compare", "a.mp4", "b.mp4", "--output", "s.jpg", "--scenes"},
		{"sheet", "--compare", "a.mp4", "b.mp4", "--output", "s.jpg", "--ssim-threshold", "2"},
		{"sheet", "--compare", "a.mp4", "b.mp4", "--output", "s.jpg", "--ssim-threshold", "0"},
		{"sheet", "--images", "frames", "--output", "s.jpg", "--count", "3"},
		{"sheet", "--images", "frames", "--output", "s.jpg", "--range", "1-2"},
		{"sheet", "--images", "frames", "--output", "s.jpg", "--label", "time"},
	} {
		var out, diagnostics bytes.Buffer
		if code := runCLI(append([]string{"--json-errors"}, args...), &out, &diagnostics); code != 2 || out.Len() != 0 || !json.Valid(diagnostics.Bytes()) || !strings.Contains(diagnostics.String(), `"code":"invalid_argument"`) {
			t.Errorf("%v code=%d %s %s", args, code, out.String(), diagnostics.String())
		}
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "sheet.jpg")
	for _, args := range [][]string{
		{"sheet", "--input", filepath.Join(dir, "absent.mp4"), "--output", output},
		{"sheet", "--images", filepath.Join(dir, "absent"), "--output", output},
		{"sheet", "--input", "a.mp4", "--output", output, "--scenes", filepath.Join(dir, "absent.json")},
	} {
		var out, diagnostics bytes.Buffer
		if code := runCLI(append([]string{"--json-errors"}, args...), &out, &diagnostics); code != 1 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"code":"input_not_found"`) {
			t.Errorf("%v code=%d %s %s", args, code, out.String(), diagnostics.String())
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "scenes.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := runCLI([]string{"--json-errors", "sheet", "--input", "a.mp4", "--output", output, "--scenes", filepath.Join(dir, "scenes.json")}, &out, &diagnostics); code != 2 || !strings.Contains(diagnostics.String(), `"code":"invalid_argument"`) {
		t.Fatalf("malformed scene analysis: %d %s", code, diagnostics.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("refused sheets left files behind: %v", entries)
	}
	out.Reset()
	diagnostics.Reset()
	if code := runCLI([]string{"--json-errors", "sheet", "--help"}, &out, &diagnostics); code != 0 || !strings.Contains(out.String()+diagnostics.String(), "-tile-width") {
		t.Fatal("sheet help unavailable", code)
	}
	out.Reset()
	if err := runArgs([]string{"--help"}, &out, &out); err != nil || !strings.Contains(out.String(), "cutmy sheet --input") {
		t.Fatal("general help does not mention sheets", err)
	}
}

func TestSheetCLIArgumentForms(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{"--images a.png b.png --output s.jpg", "--images=a.png --images=b.png --output s.jpg"},
		{"--output s.jpg --images frames", "--output s.jpg --images=frames"},
		{"--compare a.mp4 b.mp4 --every 2s", "--compare=a.mp4 --compare=b.mp4 --every 2s"},
		{"-compare a.mp4 b.mp4", "--compare=a.mp4 --compare=b.mp4"},
		{"--compare=a.mp4 --compare=b.mp4", "--compare=a.mp4 --compare=b.mp4"},
		{"--input a.mp4 --scenes --cols 4", "--input a.mp4 --scenes --cols 4"},
		{"--input a.mp4 --scenes analysis.json --cols 4", "--input a.mp4 --scenes=analysis.json --cols 4"},
		{"--scenes one.json two.json", "--scenes=one.json two.json"},
		{"--input a.mp4 --scenes", "--input a.mp4 --scenes"},
		{"--images", "--images"},
		{"--input a.mp4 b.mp4", "--input a.mp4 b.mp4"},
		{"--images a.png -- --compare x y", "--images=a.png -- --compare x y"},
	} {
		if got := strings.Join(sheetArguments(strings.Fields(tc.args)), " "); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.args, got, tc.want)
		}
	}
	for value, want := range map[string]int64{"2s": 2000, "500ms": 500, "1m": 60000, "2": 2000, "0.5": 500, "1.5s": 1500, "00:01:00": 60000} {
		if got, err := parseSheetInterval(value); err != nil || got != want {
			t.Errorf("interval %q: %d %v, want %d", value, got, err, want)
		}
	}
	for value, want := range map[string][2]int64{"10-20": {10000, 20000}, "00:01:00-00:02:30.5": {60000, 150500}, "5-": {5000, 0}, "-7.25": {0, 7250}, "0-1": {0, 1000}} {
		if start, end, err := parseSheetRange(value); err != nil || start != want[0] || end != want[1] {
			t.Errorf("range %q: %d %d %v, want %v", value, start, end, err, want)
		}
	}
}

func TestSheetCLIWorkflow(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	t.Setenv("DATABASE_URL", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	input := filepath.Join(dir, "recording.mp4")
	fixture := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=black:s=160x90:r=25:d=2", "-f", "lavfi", "-i", "color=white:s=160x90:r=25:d=2",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", input)
	if raw, err := fixture.CombinedOutput(); err != nil {
		t.Fatalf("sheet fixture: %v %s", err, raw)
	}
	sheet := func(args ...string) engine.SheetResult {
		t.Helper()
		var result engine.SheetResult
		if err := json.Unmarshal(interfacesCLI(t, append([]string{"sheet"}, args...)...), &result); err != nil || result.Version != 1 {
			t.Fatal(result, err)
		}
		for _, page := range result.Pages {
			if stat, err := os.Stat(page.Path); err != nil || stat.Size() != page.SizeBytes {
				t.Fatalf("page %+v was not published: %v", page, err)
			}
		}
		return result
	}
	output := filepath.Join(dir, "sheet.jpg")
	result := sheet("--input", input, "--output", output)
	if len(result.Cells) != 24 || len(result.Pages) != 1 || result.Pages[0].Path != output || result.Columns != 8 || result.TileWidth != 320 || result.TileHeight != 180 || result.Cells[0].Label != "00:00.083" {
		t.Fatalf("default sheet: %+v", result)
	}
	result = sheet("--input", input, "--input", input, "--range", "1-3", "--every", "500ms", "--cols", "4", "--max-rows", "1", "--tile-width", "128",
		"--label", "index,name", "--format", "png", "--threads", "2", "--timeout", "30s", "--output", filepath.Join(dir, "paged.png"))
	if len(result.Cells) != 8 || len(result.Pages) != 2 || filepath.Base(result.Pages[1].Path) != "paged-002.png" || *result.Cells[3].TimeMS != 2500 || result.Cells[4].Input != 2 || result.Cells[4].Label != "#5 recording.mp4" {
		t.Fatalf("paged sheet: %+v", result)
	}
	result = sheet("--input", input, "--at", "0,00:00:03.5,1.25", "--label", "none", "--output", filepath.Join(dir, "at.jpg"))
	if len(result.Cells) != 3 || *result.Cells[1].TimeMS != 3500 || *result.Cells[2].FrameTimeMS != 1240 || result.LabelHeight != 0 {
		t.Fatalf("explicit times: %+v", result)
	}
	result = sheet("--compare", input, input, "--count", "3", "--ssim-threshold", "0.99", "--output", filepath.Join(dir, "compare.jpg"))
	if result.Mode != "compare" || len(result.Pairs) != 3 || len(result.Cells) != 6 || len(result.BelowThreshold) != 0 || result.Pairs[1].SSIM != 1 || result.SSIMThreshold != 0.99 {
		t.Fatalf("comparison: %+v", result)
	}
	result = sheet("--input", input, "--scenes", "--output", filepath.Join(dir, "scenes.jpg"))
	if len(result.Cells) != 2 || *result.Cells[0].TimeMS != 1000 || *result.Cells[1].TimeMS != 3000 {
		t.Fatalf("detected scenes: %+v", result)
	}
	analysis := filepath.Join(dir, "analysis.json")
	if err := os.WriteFile(analysis, interfacesCLI(t, "analyze", "video", "--input", input), 0600); err != nil {
		t.Fatal(err)
	}
	result = sheet("--input", input, "--scenes", analysis, "--output", filepath.Join(dir, "analysed.jpg"))
	if len(result.Cells) != 2 || *result.Cells[0].TimeMS != 1000 || *result.Cells[1].TimeMS != 3000 {
		t.Fatalf("supplied scenes: %+v", result)
	}
	// The published sheets are images themselves.
	result = sheet("--images", output, filepath.Join(dir, "paged-001.png"), "--tile-width", "64", "--output", filepath.Join(dir, "images.jpg"))
	if result.Mode != "images" || len(result.Cells) != 2 || result.Cells[0].Path != filepath.Join(dir, "paged-001.png") || result.Cells[1].Label != "sheet.jpg" {
		t.Fatalf("image sheet: %+v", result)
	}
	before, _ := os.ReadFile(output)
	var out, diagnostics bytes.Buffer
	if code := runCLI([]string{"--json-errors", "sheet", "--input", input, "--output", output}, &out, &diagnostics); code != 1 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"code":"output_exists"`) {
		t.Fatalf("existing sheet: %d %s %s", code, out.String(), diagnostics.String())
	}
	if after, _ := os.ReadFile(output); !bytes.Equal(before, after) {
		t.Fatal("existing sheet was replaced")
	}
	out.Reset()
	diagnostics.Reset()
	if code := runCLI([]string{"--json-errors", "sheet", "--input", input, "--at", "4", "--output", filepath.Join(dir, "late.jpg")}, &out, &diagnostics); code != 2 || !strings.Contains(diagnostics.String(), `"code":"invalid_argument"`) {
		t.Fatalf("time at the source duration: %d %s", code, diagnostics.String())
	}
	if _, err := os.Lstat(filepath.Join(dir, "late.jpg")); !os.IsNotExist(err) {
		t.Fatal(fmt.Sprint("refused sheet was published: ", err))
	}
}
