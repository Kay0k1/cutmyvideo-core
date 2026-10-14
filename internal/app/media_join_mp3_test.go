package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ownedMP3Fixture(t *testing.T, ctx context.Context, c Config, rate, channels int, durationMS int64, name string) string {
	t.Helper()
	path := filepath.Join(c.DataDir, name+".mp3")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=733:sample_rate=%d:duration=%s", rate, seconds(durationMS)), "-ac", strconv.Itoa(channels), "-c:a", "libmp3lame", "-b:a", "192k", path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOwnedMP3MetadataMatchesFreshFFprobe(t *testing.T) {
	c, _ := mcConfig(t)
	for _, rate := range []int{32000, 44100, 48000} {
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d-%d", rate, channels), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				path := ownedMP3Fixture(t, ctx, c, rate, channels, 1500, fmt.Sprintf("mp3-%d-%d", rate, channels))
				stat, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				want, wantDuration, err := probe(ctx, c, path, false)
				if err != nil {
					t.Fatal(err)
				}
				actual, duration, supported, err := probeOwnedMP3(ctx, path, stat, want.ownedMP3DurationMode())
				if err != nil || !supported {
					t.Fatalf("native own-CBR fixture did not enter bounded verification: %v supported=%v", err, supported)
				}
				// Match the independent configured FFprobe family exactly, including
				// its raw/gapless duration and native rounding.
				a, b := actual.selectedAudio(), want.selectedAudio()
				if a == nil || b == nil || actual.Format.Duration != want.Format.Duration || duration != wantDuration || a.CodecName != b.CodecName || a.SampleRate != b.SampleRate || a.Channels != b.Channels || a.TimeBase != b.TimeBase {
					t.Fatalf("bounded metadata differs from independent FFprobe: %+v/%d want %+v/%d", actual, duration, want, wantDuration)
				}
				mcRun(t, ctx, c, "-xerror", "-i", path, "-f", "null", "-")
			})
		}
	}
	t.Run("half-millisecond-48000", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		path := filepath.Join(c.DataDir, "half-millisecond.mp3")
		if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=48000:duration=0.5005", "-ac", "1", "-c:a", "libmp3lame", "-b:a", "192k", path); err != nil {
			t.Fatal(err)
		}
		want, wantDuration, err := probe(ctx, c, path, false)
		if err != nil {
			t.Fatal(err)
		}
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		actual, duration, supported, err := probeOwnedMP3(ctx, path, stat, want.ownedMP3DurationMode())
		if err != nil || !supported || actual.Format.Duration != want.Format.Duration || duration != wantDuration {
			t.Fatalf("half-ms native rounding changed: bounded%+v/%d native%+v/%d supported%v err%v", actual, duration, want, wantDuration, supported, err)
		}
		if want.ownedMP3DurationMode() == ownedMP3DurationGapless && want.Format.Duration != "0.500500" {
			t.Fatal("native half-ms fixture did not reach the rounding boundary", want.Format.Duration)
		}
		mcRun(t, ctx, c, "-xerror", "-i", path, "-f", "null", "-")
	})
}

func TestJoinedMP3UsesFreshBoundedVerificationAndLargeFallback(t *testing.T) {
	c, ctx := mcConfig(t)
	input := filepath.Join(c.DataDir, "input.wav")
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=44100:duration=2", "-c:a", "pcm_s16le", input)
	commands := countMediaCommands(t, &c)
	request := ExportRequest{Format: "mp3", Quality: "best", CutMode: "accurate", OutputMode: "joined"}
	out := filepath.Join(c.DataDir, "joined.mp3")
	if _, err := ExportJoinedLocal(ctx, c, input, out, []Range{{StartMS: 200, EndMS: 700}, {StartMS: 900, EndMS: 1400}}, request); err != nil {
		t.Fatal(err)
	}
	probes := 0
	for _, line := range commands() {
		if strings.HasPrefix(line, "ffprobe ") {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("expected one source inspection and fresh Go result verification; FFprobe count=%d", probes)
	}
	// A valid output exceeding the 1MiB fast bound must keep native metadata
	// verification rather than estimate its duration or trust a previous result.
	large := ownedMP3Fixture(t, ctx, c, 44100, 1, 45000, "large")
	stat, err := os.Stat(large)
	if err != nil || stat.Size() <= ownedMP3ProbeMaxBytes {
		t.Fatalf("large native fixture did not exceed the bound: %v", err)
	}
	before := len(commands())
	if _, err := verifyJoinedOwnedMP3(ctx, c, large, request, 45000, c.MaxOutputBytes, ownedMP3DurationRaw); err != nil {
		t.Fatal(err)
	}
	calls := commands()[before:]
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "ffprobe ") {
		t.Fatalf("large own output bypassed fresh fallback: %v", calls)
	}
	mcRun(t, ctx, c, "-xerror", "-i", large, "-f", "null", "-")
}

func TestOwnedMP3UnprovedBytesKeepNativeVerification(t *testing.T) {
	c, ctx := mcConfig(t)
	path := ownedMP3Fixture(t, ctx, c, 44100, 1, 2000, "valid-mutation-source")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info := bytes.Index(original, []byte("Info"))
	if info < 0 || info+156 >= len(original) {
		t.Fatal("independent native fixture has no expected CBR Info tag")
	}
	// Locate compressed-frame boundaries with native packet inspection, not
	// the production scanner under test. The midpoint header is then damaged.
	packetJSON, err := runCommand(ctx, c.FFprobe, "-v", "error", "-select_streams", "a:0", "-show_packets", "-show_entries", "packet=pos,size", "-of", "json", path)
	if err != nil {
		t.Fatal(err)
	}
	var packetData struct {
		Packets []struct {
			Pos string `json:"pos"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(packetJSON, &packetData); err != nil || len(packetData.Packets) < 4 {
		t.Fatal("native packet boundaries unavailable", err)
	}
	middle, err := strconv.Atoi(packetData.Packets[len(packetData.Packets)/2].Pos)
	if err != nil || middle < 0 || middle+32 >= len(original) {
		t.Fatal("native midpoint frame position invalid", err)
	}
	mutations := []struct {
		name string
		edit func([]byte) []byte
	}{
		{"frame-counter", func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[info+8:], binary.BigEndian.Uint32(b[info+8:])+1)
			return b
		}},
		{"byte-counter", func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[info+12:], binary.BigEndian.Uint32(b[info+12:])-1)
			return b
		}},
		{"truncated-frame", func(b []byte) []byte { return b[:len(b)-1] }},
		{"middle-header", func(b []byte) []byte { b[middle] ^= 128; return b }},
		{"payload-crc", func(b []byte) []byte { b[middle+30] ^= 1; return b }},
		{"vbr-tag", func(b []byte) []byte { copy(b[info:], "Xing"); return b }},
		{"id3-unsync", func(b []byte) []byte { b[5] |= 128; return b }},
		{"unknown-id3-frame", func(b []byte) []byte { copy(b[10:14], "APIC"); return b }},
		{"trailing-junk", func(b []byte) []byte { return append(b, 'x') }},
	}
	wrapped := c
	commands := countMediaCommands(t, &wrapped)
	seenCalls := 0
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			caseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			changed := filepath.Join(c.DataDir, mutation.name+".mp3")
			if err := os.WriteFile(changed, mutation.edit(bytes.Clone(original)), 0600); err != nil {
				t.Fatal(err)
			}
			stat, err := os.Stat(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, supported, err := probeOwnedMP3(caseCtx, changed, stat, ownedMP3DurationRaw); err != nil || supported {
				t.Fatalf("unproved file was optimistically accepted: supported=%v err=%v", supported, err)
			}
			legacy, legacyErr := verifyJoined(caseCtx, c, changed, ExportRequest{Format: "mp3"}, 2000, c.MaxOutputBytes)
			actual, actualErr := verifyJoinedOwnedMP3(caseCtx, wrapped, changed, ExportRequest{Format: "mp3"}, 2000, c.MaxOutputBytes, ownedMP3DurationRaw)
			allCalls := commands()
			calls := allCalls[seenCalls:]
			seenCalls = len(allCalls)
			if len(calls) != 1 || !strings.HasPrefix(calls[0], "ffprobe ") || (actualErr == nil) != (legacyErr == nil) {
				t.Fatalf("unproved result did not retain native validation: calls%v actual%v legacy%v", calls, actualErr, legacyErr)
			}
			if legacyErr == nil && actual.Format.Duration != legacy.Format.Duration {
				t.Fatal("fallback duration differs from the unchanged native path")
			}
		})
	}
}

// These are valid native files, not malformed substitutes. Unsupported encoder
// shapes must remain playable and keep precisely the established fresh probe.
func TestOwnedMP3ValidUnsupportedShapesUseFreshFallback(t *testing.T) {
	c, _ := mcConfig(t)
	for _, name := range []string{"vbr", "mpeg2"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			path := filepath.Join(c.DataDir, name+".mp3")
			args := []string{"-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=44100:duration=2", "-c:a", "libmp3lame"}
			if name == "vbr" {
				args = append(args, "-q:a", "2")
			} else {
				args = append(args, "-ar", "22050", "-b:a", "128k")
			}
			if _, err := runCommand(ctx, c.FFmpeg, append(args, path)...); err != nil {
				t.Fatal(err)
			}
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, supported, err := probeOwnedMP3(ctx, path, stat, ownedMP3DurationRaw); err != nil || supported {
				t.Fatal("unproved native shape entered fast verification", err)
			}
			want, err := verifyJoined(ctx, c, path, ExportRequest{Format: "mp3"}, 2000, c.MaxOutputBytes)
			if err != nil {
				t.Fatal("valid unsupported shape failed its original verification", err)
			}
			wrapped := c
			commands := countMediaCommands(t, &wrapped)
			got, err := verifyJoinedOwnedMP3(ctx, wrapped, path, ExportRequest{Format: "mp3"}, 2000, c.MaxOutputBytes, ownedMP3DurationRaw)
			if err != nil || got.Format.Duration != want.Format.Duration {
				t.Fatal("valid unsupported shape changed native validation", err)
			}
			calls := commands()
			if len(calls) != 1 || !strings.HasPrefix(calls[0], "ffprobe ") {
				t.Fatal("valid unsupported shape skipped fresh FFprobe", calls)
			}
			mcRun(t, ctx, c, "-xerror", "-i", path, "-f", "null", "-")
		})
	}
}

type ownedMP3CountingReader struct {
	reader         io.Reader
	reads, total   int
	largestRequest int
	cancel         context.CancelFunc
}

func (r *ownedMP3CountingReader) Read(data []byte) (int, error) {
	r.reads++
	r.largestRequest = max(r.largestRequest, len(data))
	n, err := r.reader.Read(data)
	r.total += n
	if r.cancel != nil {
		r.cancel()
	}
	return n, err
}

func TestOwnedMP3ReaderKeepsIOBoundAndCancellation(t *testing.T) {
	c, ctx := mcConfig(t)
	path := ownedMP3Fixture(t, ctx, c, 44100, 1, 2000, "reader-native")
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= ownedMP3ProbeBufferBytes {
		t.Fatal("native reader fixture must span several bounded buffers", err)
	}
	reader := &ownedMP3CountingReader{reader: bytes.NewReader(data)}
	if _, supported, err := readOwnedMP3(ctx, reader, int64(len(data))); err != nil || !supported {
		t.Fatal("bounded native fixture inspection failed", err)
	}
	if reader.largestRequest > ownedMP3ProbeBufferBytes || reader.total != len(data) {
		t.Fatalf("scan exceeded its bounded fresh read: request%d total%d/%d", reader.largestRequest, reader.total, len(data))
	}
	large := &ownedMP3CountingReader{reader: bytes.NewReader(data)}
	if _, supported, err := readOwnedMP3(ctx, large, ownedMP3ProbeMaxBytes+1); err != nil || supported || large.reads != 0 {
		t.Fatal("over-bound input was read or accepted", err, large.reads)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	pre := &ownedMP3CountingReader{reader: bytes.NewReader(data)}
	if _, supported, err := readOwnedMP3(cancelCtx, pre, int64(len(data))); !errors.Is(err, context.Canceled) || supported || pre.reads != 0 {
		t.Fatal("pre-canceled inspection performed work or lost its cause", err)
	}
	midCtx, cancelMid := context.WithCancel(context.Background())
	defer cancelMid()
	mid := &ownedMP3CountingReader{reader: bytes.NewReader(data), cancel: cancelMid}
	if _, supported, err := readOwnedMP3(midCtx, mid, int64(len(data))); !errors.Is(err, context.Canceled) || supported || mid.total > ownedMP3ProbeBufferBytes {
		t.Fatal("in-scan cancellation was ignored or continued reading", err, mid.total)
	}
	if crc := ownedMP3CRC(0, []byte("123456789")); crc != 0xbb3d {
		t.Fatalf("independent CRC16/ANSI-LE check vector differs: %04x", crc)
	}
}

func TestOwnedMP3UnknownProbeFamilyKeepsFreshVerification(t *testing.T) {
	c, ctx := mcConfig(t)
	path := ownedMP3Fixture(t, ctx, c, 44100, 1, 2000, "unknown-probe-family")
	want, err := verifyJoined(ctx, c, path, ExportRequest{Format: "mp3"}, 2000, c.MaxOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "future", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			unproved := want
			unproved.LibraryVersions = append(unproved.LibraryVersions[:0:0], want.LibraryVersions...)
			switch name {
			case "missing":
				unproved.LibraryVersions = nil
			case "future":
				for i := range unproved.LibraryVersions {
					if unproved.LibraryVersions[i].Name == "libavformat" {
						unproved.LibraryVersions[i].Major = 64
					}
				}
			case "duplicate":
				for _, library := range want.LibraryVersions {
					if library.Name == "libavformat" {
						unproved.LibraryVersions = append(unproved.LibraryVersions, library)
					}
				}
			}
			wrapped := c
			commands := countMediaCommands(t, &wrapped)
			got, err := verifyJoinedOwnedMP3(ctx, wrapped, path, ExportRequest{Format: "mp3"}, 2000, c.MaxOutputBytes, unproved.ownedMP3DurationMode())
			calls := commands()
			if err != nil || got.Format.Duration != want.Format.Duration || len(calls) != 1 || !strings.HasPrefix(calls[0], "ffprobe ") {
				t.Fatalf("unproved probe family did not retain fresh native verification: calls%v result%+v err%v", calls, got, err)
			}
		})
	}
}
