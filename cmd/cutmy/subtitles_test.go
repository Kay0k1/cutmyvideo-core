package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func TestCLIRetimeSubtitlesWithoutToolsOrDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("FFMPEG_PATH", "/absent/ffmpeg")
	t.Setenv("FFPROBE_PATH", "/absent/ffprobe")
	request := engine.SubtitleRetimeRequest{Version: 1, Format: "srt", Text: "1\n00:00:05,000 --> 00:00:07,000\nHello\n", Outputs: []engine.SubtitleRetimeClip{{ClipID: "answer", Segments: []engine.Segment{{SourceStartMS: 6000, SourceEndMS: 7000, OutputEndMS: 1000}}}}}
	body, _ := json.Marshal(request)
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := runCLI([]string{"--json-errors", "subtitles", "retime", "--request", path, "--timeout", "1s"}, &out, &errout); code != 0 {
		t.Fatalf("CLI %d: %s", code, errout.String())
	}
	var result engine.SubtitleRetimeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Outputs) != 1 || result.Outputs[0].Text != "1\n00:00:00,000 --> 00:00:01,000\nHello\n\n" {
		t.Fatalf("CLI result %s / %v", out.String(), err)
	}
	for _, timeout := range []string{"0", "-1s"} {
		out.Reset()
		errout.Reset()
		if code := runCLI([]string{"--json-errors", "subtitles", "retime", "--request", path, "--timeout", timeout}, &out, &errout); code != 2 || out.Len() != 0 || !json.Valid(errout.Bytes()) {
			t.Fatal("invalid subtitle timeout accepted", code)
		}
	}
	for _, data := range []string{string(body) + "{}", `{"unknown":true}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		errout.Reset()
		if code := runCLI([]string{"--json-errors", "subtitles", "retime", "--request", path}, &out, &errout); code != 2 || out.Len() != 0 || !json.Valid(errout.Bytes()) {
			t.Fatalf("invalid request status/output %d %s %s", code, out.String(), errout.String())
		}
	}
}
