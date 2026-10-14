package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPreflightPlanMatchesExportAndLeavesNoFiles(t *testing.T) {
	input := fixture(t)
	before, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 10 << 20})
	for _, tc := range []struct{ format, mode, outputMode string }{
		{"mp4", "accurate", "separate"}, {"mp4", "accurate", "joined"},
		{"mp3", "accurate", "joined"}, {"mp4", "copy", "joined"}, {"mkv", "copy", "separate"},
	} {
		t.Run(tc.format+"-"+tc.mode+"-"+tc.outputMode, func(t *testing.T) {
			dir := t.TempDir()
			output := dir
			if tc.outputMode == "joined" {
				output = filepath.Join(dir, "joined."+tc.format)
			}
			plan := Plan{Version: 1, Input: input, OutputMode: tc.outputMode,
				Ranges:  []NamedRange{{Label: " Later / take ", StartMS: 2200, EndMS: 3000}, {Label: "repeat", StartMS: 200, EndMS: 1000}},
				Options: AdvancedOptions{Options: Options{Format: tc.format, CutMode: tc.mode}}}
			original, _ := json.Marshal(plan)
			checked, err := e.PreflightPlan(context.Background(), plan, output)
			if err != nil {
				t.Fatal(err)
			}
			if checked.Version != 1 || !checked.Valid || checked.RequestedMS != 1600 || checked.Source.DurationMS < 3900 || len(checked.TrackManifest) != 2 {
				t.Fatalf("incomplete report: %+v", checked)
			}
			if checked.Request.Quality != "best" || checked.Request.Ranges[0].Label != "Later / take" {
				t.Fatalf("defaults/labels: %+v", checked.Request)
			}
			if checked.OutputCount != len(checked.OutputPaths) || len(checked.Warnings) < 4 {
				t.Fatalf("counts/deferred checks: %+v", checked)
			}
			for _, c := range checked.Checks {
				if c.Status != "passed" && c.Status != "deferred" {
					t.Fatalf("unknown check status: %+v", c)
				}
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("preflight created files: %v %v", files, err)
			}
			afterPlan, _ := json.Marshal(plan)
			if !bytes.Equal(original, afterPlan) {
				t.Fatal("preflight mutated reusable plan")
			}
			actual, err := e.ExecutePlan(context.Background(), plan, output)
			if err != nil {
				t.Fatal(err)
			}
			if len(actual.Outputs) != checked.OutputCount {
				t.Fatalf("output count disagrees: %+v %+v", checked, actual)
			}
			for i, o := range actual.Outputs {
				if o.Path != checked.OutputPaths[i] || !reflect.DeepEqual(o.TrackManifest, checked.TrackManifest) {
					t.Fatalf("preflight/export disagree: %+v %+v", checked, actual)
				}
			}
		})
	}
	after, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source changed", err)
	}
}

func TestPreflightPlanInvalidSettingsAndOutputConflicts(t *testing.T) {
	input := fixture(t)
	e := New(Config{FFmpegThreads: 1})
	base := Plan{Version: 1, Input: input, OutputMode: "joined", Ranges: []NamedRange{{EndMS: 1000}}}
	for _, tc := range []struct {
		name   string
		change func(*Plan)
		code   ErrorCode
	}{
		{"version", func(p *Plan) { p.Version = 2 }, CodeInvalidArgument},
		{"range", func(p *Plan) { p.Ranges = []NamedRange{{EndMS: 5000}} }, CodeInvalidArgument},
		{"mode", func(p *Plan) { p.OutputMode = "typo" }, CodeInvalidArgument},
		{"audio", func(p *Plan) { index := 99; p.Options.AudioTrack = &index }, CodeAudioTrackUnavailable},
		{"silent-audio", func(p *Plan) { p.Options.NoAudio = true; p.Options.Format = "mp3" }, CodeInvalidArgument},
		{"copy-crop", func(p *Plan) { p.Options.CutMode = "copy"; p.Options.Crop = &Crop{X: 0, Y: 0, Width: 1, Height: 1} }, CodeInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := base
			tc.change(&plan)
			dir := t.TempDir()
			if _, err := e.PreflightPlan(context.Background(), plan, filepath.Join(dir, "joined.mp4")); CodeOf(err) != tc.code {
				t.Fatalf("code=%s error=%v", CodeOf(err), err)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("invalid plan created files")
			}
		})
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "joined.mp4")
	if err := os.WriteFile(output, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PreflightPlan(context.Background(), base, output); CodeOf(err) != CodeOutputExists || !errors.Is(err, os.ErrExist) {
		t.Fatal("existing output not classified", err)
	}
	content, _ := os.ReadFile(output)
	if string(content) != "keep" {
		t.Fatal("preflight changed existing output")
	}
	if _, err := e.PreflightPlan(context.Background(), base, filepath.Join(dir, "absent", "out.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing parent accepted", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight created output directory")
	}
}

func TestPreflightPlanCancellationAndToolErrors(t *testing.T) {
	input := fixture(t)
	plan := Plan{Version: 1, Input: input, Ranges: []NamedRange{{EndMS: 1000}}}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Config{}).PreflightPlan(ctx, plan, dir); CodeOf(err) != CodeCancelled || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, err := New(Config{InspectTimeout: time.Nanosecond}).PreflightPlan(context.Background(), plan, dir); CodeOf(err) != CodeTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout lost", err)
	}
	for _, config := range []Config{
		{FFmpegPath: filepath.Join(dir, "missing-ffmpeg")}, {FFprobePath: filepath.Join(dir, "missing-ffprobe")},
	} {
		if _, err := New(config).PreflightPlan(context.Background(), plan, dir); CodeOf(err) != CodeToolUnavailable {
			t.Fatalf("tool failure misclassified: %v", err)
		}
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("failed preflight created files")
	}
}
