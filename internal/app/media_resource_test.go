//go:build !windows

package app

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProbeRejectsResourceExplosionBeforeExport(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		width, height, streams int
		valid                  bool
	}{{"8K", 7680, 4320, 1, true}, {"huge", math.MaxInt, 2, 1, false}, {"zero", 100, 0, 1, false}, {"streams", 160, 90, 33, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var streams []map[string]any
			for range tc.streams {
				streams = append(streams, map[string]any{"codec_type": "video", "width": tc.width, "height": tc.height})
			}
			response, _ := json.Marshal(map[string]any{"format": map[string]string{"duration": "1"}, "streams": streams})
			// Exercise the real subprocess/JSON boundary without allocating a huge frame.
			probePath := filepath.Join(t.TempDir(), "probe")
			script := "#!/bin/sh\nprintf '%s' '" + string(response) + "'\n"
			if err := os.WriteFile(probePath, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			_, _, err := probe(context.Background(), Config{FFprobe: probePath}, "fixture.mp4", false)
			if (err == nil) != tc.valid {
				t.Fatalf("untrusted dimensions/streams accepted=%v err=%v", err == nil, err)
			}
		})
	}
}

func TestDecoderThreadsAreBoundedForDefaultAndExplicitConfiguration(t *testing.T) {
	for _, tc := range []struct{ input, want int }{{0, 2}, {-1, 2}, {1, 1}, {32, 32}, {33, 2}} {
		args := mediaInputBounds(Config{FFmpegThreads: tc.input})
		if strings.Join(args[:2], " ") != "-threads "+strconv.Itoa(tc.want) {
			t.Fatal("unbounded decoder", args)
		}
	}
}
