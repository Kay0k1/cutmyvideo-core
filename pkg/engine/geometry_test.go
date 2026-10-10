package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise fresh public inspection, rather than constructing SourceInfo values:
// the coded dimensions must survive while display geometry comes from probe.
func TestEngineInspectSeparatesCodedAndDisplayDimensions(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is not installed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	base, rotated := filepath.Join(dir, "anamorphic.mp4"), filepath.Join(dir, "rotated.mp4")
	if output, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=240x120:rate=25", "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=48000", "-map", "0:v:0", "-map", "1:a:0", "-t", "1", "-vf", "setsar=2", "-c:v", "libx264", "-c:a", "aac", "-threads", "1", base).CombinedOutput(); err != nil {
		t.Fatalf("geometry fixture: %v %s", err, output)
	}
	// New FFmpeg versions use the explicit display-rotation input option; older
	// versions encode the matrix from the rotate tag when remuxing. Never skip a
	// missing rotation: the public geometry assertion below must still detect it.
	rotate := func(degrees, destination string) {
		t.Helper()
		output, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-display_rotation", degrees, "-i", base, "-map", "0", "-c", "copy", destination).CombinedOutput()
		if err != nil && strings.Contains(string(output), "Unrecognized option 'display_rotation'") {
			output, err = exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", base, "-map", "0", "-c", "copy", "-metadata:s:v:0", "rotate="+degrees, destination).CombinedOutput()
		}
		if err != nil {
			t.Fatalf("rotation fixture: %v %s", err, output)
		}
	}
	rotate("90", rotated)
	engine := New(Config{})
	for _, tc := range []struct {
		path          string
		width, height int
	}{{base, 480, 120}, {rotated, 120, 480}} {
		info, err := engine.Inspect(ctx, tc.path)
		if err != nil || info.Width != 240 || info.Height != 120 || info.DisplayWidth != tc.width || info.DisplayHeight != tc.height {
			t.Fatalf("coded/display inspection: %+v %v, want coded 240x120, display %dx%d", info, err, tc.width, tc.height)
		}
		encoded, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]int64
		if err = json.Unmarshal(encoded, &response); err != nil || response["width"] != 240 || response["height"] != 120 || response["display_width"] != int64(tc.width) || response["display_height"] != int64(tc.height) {
			t.Fatalf("public JSON lost coded/display distinction: %s %v", encoded, err)
		}
	}

	// Unsupported display geometry must not make basic inspection, original
	// packet copy or extraction of the unrelated audio stream unavailable.
	oblique := filepath.Join(dir, "oblique.mp4")
	rotate("45", oblique)
	info, err := engine.Inspect(ctx, oblique)
	if err != nil || info.Width != 240 || info.Height != 120 || info.DisplayWidth != 0 || info.DisplayHeight != 0 {
		t.Fatalf("unsupported display geometry lost coded dimensions: %+v %v", info, err)
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"display_width", "display_height"} {
		if _, ok := response[field]; ok {
			t.Fatalf("unsupported geometry invented display dimensions: %s", encoded)
		}
	}
	for _, tc := range []struct {
		name    string
		options Options
	}{{"original.mp4", Options{Format: "mp4", Quality: "best", CutMode: "copy"}}, {"audio.mp3", Options{Format: "mp3"}}} {
		result, err := engine.Export(ctx, oblique, filepath.Join(dir, tc.name), Range{EndMS: 800}, tc.options)
		if err != nil || result.SizeBytes <= 0 {
			t.Fatalf("unrelated output %s refused unsupported display geometry: %+v %v", tc.name, result, err)
		}
	}
	for _, tc := range []struct {
		name    string
		options Options
	}{{"accurate.mp4", Options{Format: "mp4"}}, {"capped-copy.mp4", Options{Format: "mp4", Quality: "720p", CutMode: "copy"}}} {
		path := filepath.Join(dir, tc.name)
		_, err := engine.Export(ctx, oblique, path, Range{EndMS: 800}, tc.options)
		if CodeOf(err) != CodeUnsupportedMedia {
			t.Fatalf("geometry-dependent output %s: code=%s err=%v", tc.name, CodeOf(err), err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refused geometry-dependent output was published: %s %v", path, err)
		}
	}

	audio := filepath.Join(dir, "audio.m4a")
	if output, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=48000", "-t", "1", "-c:a", "aac", audio).CombinedOutput(); err != nil {
		t.Fatalf("audio fixture: %v %s", err, output)
	}
	info, err = engine.Inspect(ctx, audio)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	response = nil
	if err = json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"width", "height", "display_width", "display_height"} {
		if _, ok := response[field]; ok {
			t.Fatalf("audio-only inspection invented video dimensions: %s", encoded)
		}
	}
}
