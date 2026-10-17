package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyKeyframeSelectionKeepsInitialAndDecodeClocks(t *testing.T) {
	frames := []copyKeyframe{{PTS: 40}, {PTS: 2040, DTS: 1960, HasDTS: true}, {PTS: 4040, DTS: 3960, HasDTS: true}}
	for _, test := range []struct {
		start, pts, dts int64
		hasDTS          bool
	}{
		{0, 40, 0, false}, {100, 40, 0, false}, {3500, 2040, 1960, true}, {4500, 4040, 3960, true},
	} {
		got, err := selectCopyKeyframe(frames, test.start)
		if err != nil || got.PTS != test.pts || got.DTS != test.dts || got.HasDTS != test.hasDTS {
			t.Fatalf("start %d: %+v, %v", test.start, got, err)
		}
	}
	if _, err := selectCopyKeyframe([]copyKeyframe{{PTS: 2100}}, 2200); err == nil {
		t.Fatal("a later keyframe without a decode clock was accepted")
	}
}

func TestLocalKeyframeCacheChecksSourceToolStreamAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("stable source"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, session := BeginLocalExport(context.Background(), path)
	defer session.Close()
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(t.TempDir(), "missing-ffprobe")
	session.keyframeFile = file
	session.keyframes = []keyframeWindow{{from: 0, until: 5001, stream: 2, tool: tool, frames: []copyKeyframe{{PTS: 0, DTS: -80, HasDTS: true}, {PTS: 2000, DTS: 1920, HasDTS: true}}}}
	got, err := nearestKeyframe(ctx, Config{FFprobe: tool}, path, 3500, false, 2)
	if err != nil || got.PTS != 2000 || got.DTS != 1920 {
		t.Fatalf("warm local lookup: %+v, %v", got, err)
	}
	for _, test := range []struct {
		tool   string
		stream int
		remote bool
	}{{tool + "-changed", 2, false}, {tool, 3, false}, {tool, 2, true}} {
		if _, err := nearestKeyframe(ctx, Config{FFprobe: test.tool}, path, 3500, test.remote, test.stream); err == nil {
			t.Fatal("cache crossed tool, stream or remote boundary")
		}
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := nearestKeyframe(cancelCtx, Config{FFprobe: tool}, path, 3500, false, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cache hit: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement with changed length"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nearestKeyframe(ctx, Config{FFprobe: tool}, path, 3500, false, 2); problemFromError(err) == nil || problemFromError(err).code != "source_changed" {
		t.Fatalf("changed source reused keyframes: %v", err)
	}
}

func TestLocalKeyframeLookaheadBoundsFallbackAndCacheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("stable source"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, session := BeginLocalExport(context.Background(), path)
	defer session.Close()
	session.SetRanges([]Range{{StartMS: 1000}, {StartMS: 4000}, {StartMS: 16001}, {StartMS: 72000000}})
	var calls [][2]int64
	probe := func(_ context.Context, _ Config, _ string, remote bool, _ int, from, until int64) ([]copyKeyframe, error) {
		if remote {
			t.Fatal("local cache probed a remote input")
		}
		calls = append(calls, [2]int64{from, until})
		if until > 1001 {
			return nil, errors.New("later damaged packet")
		}
		return []copyKeyframe{{PTS: 0, DTS: -80, HasDTS: true}}, nil
	}
	if _, err := session.nearestKeyframeWithProbe(ctx, Config{FFprobe: "tool"}, 1000, 0, probe); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != [2]int64{0, 4001} || calls[1] != [2]int64{0, 1001} {
		t.Fatalf("lookahead changed bounded fallback: %+v", calls)
	}
	if _, err := session.nearestKeyframeWithProbe(ctx, Config{FFprobe: "tool"}, 1000, 0, probe); err != nil || len(calls) != 2 {
		t.Fatalf("original fallback was not reusable: %+v, %v", calls, err)
	}
	session.SetRanges(nil)
	probe = func(_ context.Context, _ Config, _ string, _ bool, _ int, from, until int64) ([]copyKeyframe, error) {
		frames := make([]copyKeyframe, 300)
		for i := range frames {
			frames[i] = copyKeyframe{PTS: until - 1, DTS: until - 81, HasDTS: true}
		}
		return frames, nil
	}
	for i := int64(1); i <= 32; i++ {
		if _, err := session.nearestKeyframeWithProbe(ctx, Config{FFprobe: "tool"}, i*60000, 0, probe); err != nil {
			t.Fatal(err)
		}
	}
	entries := 0
	for _, window := range session.keyframes {
		entries += len(window.frames)
	}
	if len(session.keyframes) > sessionKeyframeWindows || entries > sessionKeyframeEntries {
		t.Fatalf("unbounded keyframe cache: %d windows, %d frames", len(session.keyframes), entries)
	}
}

func TestSubtitleIndexPreservesSpanningUnsortedRepeatedAndEmptyCues(t *testing.T) {
	cues := []subtitleCue{{StartMS: 0, EndMS: 20000, Text: "spanning"}, {StartMS: 1000, EndMS: 2500, Text: "one"}, {StartMS: 2000, EndMS: 2000, Text: "empty"}, {StartMS: 3000, EndMS: 4000, Text: "two"}, {StartMS: 6000, EndMS: 7000, Text: "three"}}
	segments := []ArtifactSegment{{SourceStartMS: 6000, SourceEndMS: 6800}, {SourceStartMS: 1500, SourceEndMS: 3500, OutputStartMS: 800}, {SourceStartMS: 6000, SourceEndMS: 6800, OutputStartMS: 2800}}
	for _, unordered := range []bool{false, true} {
		current := append([]subtitleCue(nil), cues...)
		if unordered {
			current[1], current[4] = current[4], current[1]
		}
		var expected strings.Builder
		number := 0
		for _, segment := range segments {
			for _, cue := range current {
				if cue.StartMS >= segment.SourceEndMS || cue.EndMS <= segment.SourceStartMS || cue.EndMS <= cue.StartMS {
					continue
				}
				number++
				start := max(cue.StartMS, segment.SourceStartMS) - segment.SourceStartMS + segment.OutputStartMS
				end := min(cue.EndMS, segment.SourceEndMS) - segment.SourceStartMS + segment.OutputStartMS
				fmt.Fprintf(&expected, "%d\n%s --> %s\n%s\n\n", number, subtitleTimestamp(start), subtitleTimestamp(end), cue.Text)
			}
		}
		got, err := rebaseSubtitles(current, segments)
		if err != nil || !bytes.Equal(got, []byte(expected.String())) {
			t.Fatalf("unordered=%t clipping changed: %s, %v", unordered, got, err)
		}
	}
}

func TestSourceSubtitleIndexReusesOnlyOneBoundedTrack(t *testing.T) {
	dir := t.TempDir()
	source, text := filepath.Join(dir, "source"), filepath.Join(dir, "track.srt")
	if err := os.WriteFile(source, []byte("source identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(text, []byte("1\n00:00:00,500 --> 00:00:02,800\nAlpha\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, session := BeginLocalExport(context.Background(), source)
	defer session.Close()
	session.file, _ = os.Stat(source)
	session.cues[3], session.cues[4] = text, text
	c := Config{FFmpeg: "unused"}
	first, err := sourceSubtitleIndex(ctx, c, source, probeStream{Index: 3})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sourceSubtitleIndex(ctx, c, source, probeStream{Index: 3})
	if err != nil || first != second {
		t.Fatalf("parsed source track was not reused: %v", err)
	}
	other, err := sourceSubtitleIndex(ctx, c, source, probeStream{Index: 4})
	if err != nil || other == first || session.subtitleTrack != 4 {
		t.Fatalf("track selection crossed parsed cache: %v", err)
	}
	large := "1\n00:00:00,500 --> 00:00:02,800\n" + strings.Repeat("x", subtitleIndexCacheBytes) + "\n\n"
	if err := os.WriteFile(text, []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceSubtitleIndex(ctx, c, source, probeStream{Index: 3}); err != nil {
		t.Fatal(err)
	}
	if session.subtitleIndex != nil {
		t.Fatal("large source track became a retained parsed cache")
	}
	if err := os.WriteFile(source, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceSubtitleIndex(ctx, c, source, probeStream{Index: 4}); problemFromError(err) == nil || problemFromError(err).code != "source_changed" {
		t.Fatalf("changed source reused parsed subtitles: %v", err)
	}
}

func TestCopyJoinReusesRepeatedSegmentsAndFaststartOnlyFinal(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	original := batchFixture(t, c, ctx)
	// Keep this a positive witness for reuse plus direct audio on a source
	// whose clocks already match the established millisecond staging format.
	input := filepath.Join(c.DataDir, "millisecond-source.mkv")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", original, "-map", "0", "-c", "copy", "-c:s", "srt", input); err != nil {
		t.Fatal(err)
	}
	commands := countMediaCommands(t, &c)
	packetTrim, err := supportsAudioPacketDrop(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	track := 2
	for _, format := range []string{"mp4", "mkv"} {
		source := original
		if format == "mkv" {
			source = input
		}
		request := ExportRequest{Format: format, Quality: "best", CutMode: "copy", AudioTrack: &track, SubtitleMode: "preserve"}
		ranges := []Range{{StartMS: 1200, EndMS: 2400, Label: "first"}, {StartMS: 3200, EndMS: 4400, Label: "middle"}, {StartMS: 1200, EndMS: 2400, Label: "repeat"}}
		out := filepath.Join(c.DataDir, "repeat."+format)
		segments, err := ExportJoinedLocal(ctx, c, source, out, ranges, request)
		if err != nil {
			t.Fatal(err)
		}
		if len(segments) != 3 || segments[2].Label != "repeat" || segments[2].SourceStartMS != segments[0].SourceStartMS || segments[2].OutputStartMS != segments[1].OutputEndMS {
			t.Fatalf("repeated segment manifest changed: %+v", segments)
		}
		if _, err := runCommand(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", out, "-f", "null", "-"); err != nil {
			t.Fatal(err)
		}
		for _, segment := range segments {
			compareAudioTimeline(t, ctx, c, source, out, track, segment.SourceStartMS+300, segment.OutputStartMS+300, true)
		}
	}
	var encodes, keyframes, faststarts, directAudio, stagedAudio int
	for _, line := range commands() {
		if strings.HasPrefix(line, "ffprobe ") && strings.Contains(line, "-show_packets") {
			keyframes++
		}
		if strings.HasPrefix(line, "ffmpeg ") && strings.Contains(line, "video-") && strings.Contains(line, "-output_ts_offset") {
			encodes++
		}
		if strings.Contains(line, "-movflags +faststart") {
			faststarts++
			if !strings.HasSuffix(line, "repeat.mp4") {
				t.Fatalf("intermediate MP4 kept faststart: %s", line)
			}
		}
		if strings.Contains(line, "-bsf:a noise=amount=0:drop=") {
			directAudio++
		}
		if strings.Contains(line, "audio.mka") {
			stagedAudio++
		}
	}
	if encodes != 4 || keyframes != 2 || faststarts != 1 {
		t.Fatalf("repeated work remained: encodes=%d keyframes=%d faststarts=%d", encodes, keyframes, faststarts)
	}
	if packetTrim && (directAudio != 2 || stagedAudio != 4) || !packetTrim && (directAudio != 0 || stagedAudio != 8) {
		t.Fatalf("copy audio process selection: supported=%t direct=%d staged=%d", packetTrim, directAudio, stagedAudio)
	}
}
