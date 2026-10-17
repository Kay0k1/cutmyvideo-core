package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func TestPacketDropCapabilitiesCheckToolIdentityBoundsAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, []byte("first executable identity"), 0700); err != nil {
		t.Fatal(err)
	}
	c := Config{FFmpeg: path}
	calls := 0
	probe := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte("noise AVOptions:\n amount <string>\n drop <string>\n"), nil
	}
	for range 2 {
		if available, err := supportsAudioPacketDropWithProbe(context.Background(), c, probe); err != nil || !available {
			t.Fatalf("capability query: %t,%v", available, err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged executable was probed %d times", calls)
	}
	if err := os.WriteFile(path, []byte("replacement executable with a different length"), 0700); err != nil {
		t.Fatal(err)
	}
	if available, err := supportsAudioPacketDropWithProbe(context.Background(), c, probe); err != nil || !available || calls != 2 {
		t.Fatalf("tool replacement retained capability: %t,%v,calls%d", available, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := supportsAudioPacketDropWithProbe(ctx, c, probe); !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("canceled cache hit: %v", err)
	}
	for i := range 40 {
		tool := filepath.Join(filepath.Dir(path), strings.Repeat("x", i+1)+filepath.Ext(path))
		if err := os.WriteFile(tool, []byte("bounded executable"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := supportsAudioPacketDropWithProbe(context.Background(), Config{FFmpeg: tool}, probe); err != nil {
			t.Fatal(err)
		}
	}
	count := localmedia.PacketDropCapabilityCount()
	if count > 32 {
		t.Fatalf("capability cache retained %d executables", count)
	}
	if packetDropHelpAvailable([]byte("noise AVOptions:\n amount <string>\n dropamount <int>\n")) {
		t.Fatal("legacy random-drop filter was mistaken for an expression filter")
	}
}

func TestCopyPacketTrimEligibilityPreservesUnprovenClockPaths(t *testing.T) {
	for _, sample := range []struct {
		codec, clock, format, timeBase string
		want                           bool
	}{
		{"aac", "0", "mp4", "1/1000", false}, {"mp3", "0.021", "mkv", "1/1000", true},
		{"flac", "0", "mkv", "1/1000", true}, {"opus", "0.007", "mp4", "1/1000", false},
		{"opus", "0.007", "mkv", "1/1000", false}, {"vorbis", "0", "mkv", "1/1000", false},
		{"aac", "", "mp4", "1/1000", false}, {"aac", "N/A", "mkv", "1/1000", false},
		{"aac", "NaN", "mp4", "1/1000", false}, {"aac", "+Inf", "mkv", "1/1000", false},
		{"aac", "0", "mp4", "1/48000", false}, {"aac", "0", "mp4", "1/44100", false},
		{"aac", "0", "mp4", "", false}, {"aac", "0", "mp4", "N/A", false},
		{"aac", "0", "mkv", "2/2000", true}, {"aac", "0", "mkv", "0/1000", false},
		{"aac", "0", "mp4", "1/0", false}, {"aac", "0", "mp4", "-1/-1000", false},
		{"aac", "0", "mp4", "2147483647/2147483647", false},
		{"aac", "0", "mp4", "999999999999999999999999/1000", false},
	} {
		selection := mediaSelection{Audio: &probeStream{CodecName: sample.codec, StartTime: sample.clock, TimeBase: sample.timeBase}}
		if got := copyAudioPacketTrimEligible(selection, ExportRequest{Format: sample.format}); got != sample.want {
			t.Fatalf("eligibility for %s clock=%s format=%s: %t", sample.codec, sample.clock, sample.format, got)
		}
	}
	if copyAudioPacketTrimEligible(mediaSelection{}, ExportRequest{Format: "mp4"}) {
		t.Fatal("missing audio became eligible")
	}
}

func TestCopyJoinAudioFallsBackWhenPacketTrimUnavailable(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	original := batchFixture(t, c, ctx)
	input := filepath.Join(c.DataDir, "eligible-source.mkv")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", original, "-map", "0", "-c", "copy", "-c:s", "srt", input); err != nil {
		t.Fatal(err)
	}
	commands := countMediaCommands(t, &c)
	available, err := supportsAudioPacketDropWithProbe(ctx, c, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("legacy filter without packet drop expressions"), nil
	})
	if err != nil || available {
		t.Fatalf("negative capability witness failed: available=%t err=%v", available, err)
	}
	selected := 2
	request := ExportRequest{Format: "mkv", Quality: "best", CutMode: "copy", AudioTrack: &selected}
	selection, err := probeInputSelectionOptions(ctx, c, []mediaInput{{Path: input}}, request)
	if err != nil || !copyAudioPacketTrimEligible(selection, request) {
		t.Fatalf("capability negative control does not exercise an eligible source: %v", err)
	}
	out := filepath.Join(c.DataDir, "fallback.mkv")
	segments, err := ExportJoinedLocal(ctx, c, input, out, []Range{{StartMS: 1200, EndMS: 2200}, {StartMS: 3200, EndMS: 4200}}, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		compareAudioTimeline(t, ctx, c, input, out, selected, segment.SourceStartMS+300, segment.OutputStartMS+300, true)
	}
	var stages, queries int
	for _, line := range commands() {
		if strings.Contains(line, "audio.mka") {
			stages++
		}
		if strings.Contains(line, "-h bsf=noise") {
			queries++
		}
	}
	if stages != 4 || queries != 0 {
		t.Fatalf("unsupported capability did not retain staged output: stages%d queries%d", stages, queries)
	}
}
