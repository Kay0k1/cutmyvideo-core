package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func ownedMP4Fixture(t *testing.T, ctx context.Context, c Config, rate, channels int, name string) string {
	t.Helper()
	path := filepath.Join(c.DataDir, name+".mp4")
	args := []string{"-f", "lavfi", "-i", "testsrc2=duration=1.5:size=160x96:rate=25"}
	if channels > 0 {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=733:sample_rate=%d:duration=1.5", rate))
	}
	args = append(args, "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-crf", "18", "-coder", "1", "-pix_fmt", "yuv420p")
	if channels > 0 {
		args = append(args, "-ac", strconv.Itoa(channels), "-c:a", "aac", "-b:a", "128k")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-movflags", "+faststart", path)
	mcRun(t, ctx, c, args...)
	return path
}

func TestOwnedMP4MetadataMatchesFreshFFprobe(t *testing.T) {
	c, ctx := mcConfig(t)
	for _, spec := range [][2]int{{48000, 0}, {32000, 1}, {44100, 1}, {48000, 1}, {48000, 2}} {
		t.Run(fmt.Sprintf("%d-%d", spec[0], spec[1]), func(t *testing.T) {
			path := ownedMP4Fixture(t, ctx, c, spec[0], spec[1], fmt.Sprintf("mp4-%d-%d", spec[0], spec[1]))
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			got, duration, supported, err := probeOwnedMP4(ctx, path, stat)
			if err != nil || !supported {
				t.Fatalf("owned MP4 was not admitted: supported=%v err=%v", supported, err)
			}
			want, ms, err := probe(ctx, c, path, false)
			if err != nil {
				t.Fatal(err)
			}
			if duration != ms || got.Format.Duration != want.Format.Duration || len(got.Streams) != len(want.Streams) {
				t.Fatalf("metadata differs: got=%+v/%d want=%+v/%d", got, duration, want, ms)
			}
			for i, s := range got.Streams {
				w := want.Streams[i]
				if s.Index != w.Index || s.CodecType != w.CodecType || s.CodecName != w.CodecName || s.Width != w.Width || s.Height != w.Height || s.Channels != w.Channels || s.SampleRate != w.SampleRate || s.TimeBase != w.TimeBase {
					t.Fatalf("track%d differs: got=%+v want=%+v", i, s, w)
				}
			}
			mcRun(t, ctx, c, "-xerror", "-i", path, "-f", "null", "-")
		})
	}
	t.Run("half-millisecond", func(t *testing.T) {
		path := filepath.Join(c.DataDir, "half-millisecond.mp4")
		mcRun(t, ctx, c, "-f", "lavfi", "-i", "color=black:size=2x2:rate=2000:duration=0.5005", "-an", "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-crf", "18", "-coder", "1", "-pix_fmt", "yuv420p", "-video_track_timescale", "48000", "-movie_timescale", "48000", "-movflags", "+faststart", path)
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		got, ms, supported, err := probeOwnedMP4(ctx, path, stat)
		want, nativeMS, nativeErr := probe(ctx, c, path, false)
		if err != nil || nativeErr != nil || !supported || got.Format.Duration != want.Format.Duration || ms != nativeMS {
			t.Fatalf("half-ms rounding differs: %+v/%d/%v/%v versus %+v/%d/%v", got, ms, supported, err, want, nativeMS, nativeErr)
		}
	})
}

func TestJoinedMP4UsesFreshStructuralVerification(t *testing.T) {
	c, ctx := mcConfig(t)
	c.FFmpegThreads = 2
	input := ownedMP4Fixture(t, ctx, c, 48000, 1, "input")
	commands := countMediaCommands(t, &c)
	out := filepath.Join(c.DataDir, "joined.mp4")
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined"}
	ranges := []Range{{StartMS: 960, EndMS: 1460, Label: "later"}, {StartMS: 200, EndMS: 700, Label: "earlier"}}
	segments, err := ExportJoinedLocal(ctx, c, input, out, ranges, request)
	if err != nil {
		t.Fatal(err)
	}
	joinedDiscoverySegments(t, segments, ranges)
	probes := 0
	for _, command := range commands() {
		if strings.HasPrefix(command, "ffprobe ") {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("expected one source probe, no output probe: %d", probes)
	}
	stat, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	p, ms, supported, err := probeOwnedMP4(ctx, out, stat)
	if err != nil || !supported || p.selectedAudio() == nil {
		t.Fatalf("fresh result unsupported: %+v/%d/%v/%v", p, ms, supported, err)
	}
	mcRun(t, ctx, c, "-xerror", "-i", out, "-f", "null", "-")
	selection, err := probeInputSelectionOptions(ctx, c, []mediaInput{{Path: input}}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyJoinedOwnedMP4(ctx, c, out, request, ms+351, c.MaxOutputBytes, selection); err == nil {
		t.Fatal("duration gate was weakened")
	}
	if _, err := verifyJoinedOwnedMP4(ctx, c, out, request, ms, stat.Size(), selection); !errors.Is(err, errOutputLimit) {
		t.Fatalf("size gate was weakened: %v", err)
	}
}

func TestOwnedMP4UnknownAndCorruptShapesKeepNativeVerification(t *testing.T) {
	c, ctx := mcConfig(t)
	input := ownedMP4Fixture(t, ctx, c, 48000, 1, "supported")
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined"}
	selection, err := probeInputSelectionOptions(ctx, c, []mediaInput{{Path: input}}, request)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func([]byte) []byte{
		"truncated":    func(b []byte) []byte { return b[:len(b)-73] },
		"unknown-ftyp": func(b []byte) []byte { copy(b[8:12], "qt  "); return b },
		"oversized-sample": func(b []byte) []byte {
			i := bytes.Index(b, []byte("stsz"))
			if i < 0 {
				t.Fatal("stsz missing")
			}
			binary.BigEndian.PutUint32(b[i+16:], 0x7fffffff)
			return b
		},
		"external-data-reference": func(b []byte) []byte {
			i := bytes.Index(b, []byte("url "))
			if i < 0 {
				t.Fatal("url missing")
			}
			b[i+7] = 0
			return b
		},
		"unknown-edit": func(b []byte) []byte {
			i := bytes.Index(b, []byte("elst"))
			if i < 0 {
				t.Fatal("elst missing")
			}
			binary.BigEndian.PutUint32(b[i+16:], 1)
			return b
		},
		"trailing-data": func(b []byte) []byte { return append(b, []byte("invalid!")...) },
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(c.DataDir, name+".mp4")
			b := mutation(bytes.Clone(original))
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, supported, err := probeOwnedMP4(ctx, path, stat); err != nil || supported {
				t.Fatalf("unproved structure admitted: supported=%v err=%v", supported, err)
			}
			_, nativeErr := verifyJoined(ctx, c, path, request, 1520, c.MaxOutputBytes)
			_, fastErr := verifyJoinedOwnedMP4(ctx, c, path, request, 1520, c.MaxOutputBytes, selection)
			if (nativeErr == nil) != (fastErr == nil) {
				t.Fatalf("fallback acceptance differs from native: %v/%v", nativeErr, fastErr)
			}
		})
	}
	commands := countMediaCommands(t, &c)
	selection.mp3DurationMode = ownedMP3DurationUnknown
	if _, err := verifyJoinedOwnedMP4(ctx, c, input, request, 1520, c.MaxOutputBytes, selection); err != nil {
		t.Fatal(err)
	}
	probes := 0
	for _, line := range commands() {
		if strings.HasPrefix(line, "ffprobe ") {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("unknown tool family omitted fresh native probe: %d", probes)
	}
}

func TestOwnedMP4FreshIdentityBoundsAndCancellation(t *testing.T) {
	c, ctx := mcConfig(t)
	path := ownedMP4Fixture(t, ctx, c, 48000, 1, "identity")
	handle, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := handle.Stat()
	if closeErr := handle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(c.DataDir, "replacement.mp4")
	if err := os.WriteFile(replacement, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	// Windows does not replace an existing destination with Rename.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, _, supported, err := probeOwnedMP4(ctx, path, before); err != nil || supported {
		t.Fatalf("replaced same-size/time inode admitted: %v/%v", supported, err)
	}
	before, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := probeOwnedMP4(cancelled, path, before); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
	r := ownedMP4Reader{ctx: ctx, file: bytes.NewReader(b), size: int64(len(b)), read: ownedMP4MaxReadBytes - 4}
	if _, _, supported := r.inspect(); supported || r.read > ownedMP4MaxReadBytes {
		t.Fatalf("read budget exceeded or incomplete scan admitted: %d/%v", r.read, supported)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(ownedMP4MaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	large, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, supported, err := probeOwnedMP4(ctx, path, large); err != nil || supported {
		t.Fatalf("oversize file admitted: %v/%v", supported, err)
	}
}

func TestOwnedMP4TrackAndEditRoundingMatchesFFprobe(t *testing.T) {
	c, ctx := mcConfig(t)
	path := filepath.Join(c.DataDir, "fractional-audio.mp4")
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "testsrc2=duration=1.503:size=160x96:rate=25", "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=44100:duration=1.503", "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-crf", "18", "-coder", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", path)
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ms, supported, err := probeOwnedMP4(ctx, path, stat)
	want, nativeMS, nativeErr := probe(ctx, c, path, false)
	if err != nil || nativeErr != nil || !supported || got.Format.Duration != want.Format.Duration || ms != nativeMS {
		t.Fatalf("fractional track rounding differs: %+v/%d/%v/%v versus %+v/%d/%v", got, ms, supported, err, want, nativeMS, nativeErr)
	}
	input := path
	wrapped := c
	commands := countMediaCommands(t, &wrapped)
	wrapped.FFmpegThreads = 2
	var ranges []Range
	for i := 0; i < 32; i++ {
		start := int64(200)
		if i%2 != 0 {
			start = 320
		}
		ranges = append(ranges, Range{StartMS: start, EndMS: start + 1100, Label: fmt.Sprint(i)})
	}
	out := filepath.Join(c.DataDir, "joined32.mp4")
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined"}
	if _, err := ExportJoinedLocal(ctx, wrapped, input, out, ranges, request); err != nil {
		t.Fatal(err)
	}
	probes := 0
	for _, command := range commands() {
		if strings.HasPrefix(command, "ffprobe ") {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("reordered32 intervals fell back to a probe: %d", probes)
	}
	stat, err = os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	got, ms, supported, err = probeOwnedMP4(ctx, out, stat)
	want, nativeMS, nativeErr = probe(ctx, c, out, false)
	if err != nil || nativeErr != nil || !supported || ms != nativeMS || got.Format.Duration != want.Format.Duration {
		t.Fatalf("reordered32 duration differs: %d/%v/%v versus %d/%v", ms, supported, err, nativeMS, nativeErr)
	}
	mcRun(t, ctx, c, "-xerror", "-i", out, "-f", "null", "-")
}
