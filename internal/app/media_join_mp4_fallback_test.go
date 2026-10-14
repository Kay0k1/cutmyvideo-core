package app

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnedMP4ValidUnsupportedShapesUseFreshFallback(t *testing.T) {
	c, ctx := mcConfig(t)
	input := ownedMP4Fixture(t, ctx, c, 48000, 1, "original")
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined"}
	selection, err := probeInputSelectionOptions(ctx, c, []mediaInput{{Path: input}}, request)
	if err != nil {
		t.Fatal(err)
	}
	fragmented := filepath.Join(c.DataDir, "fragmented.mp4")
	mcRun(t, ctx, c, "-i", input, "-c", "copy", "-movflags", "+frag_keyframe+empty_moov", fragmented)
	compact := filepath.Join(c.DataDir, "compact.mp4")
	mcRun(t, ctx, c, "-i", input, "-c:v", "libx264", "-threads", "2", "-preset", "veryfast", "-crf", "20", "-c:a", "copy", "-movflags", "+faststart", compact)
	large := filepath.Join(c.DataDir, "large.mp4")
	b, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(large, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	var box [8]byte
	padding := ownedMP4MaxFileBytes + 1024 - int64(len(b))
	binary.BigEndian.PutUint32(box[:4], uint32(padding))
	copy(box[4:], "free")
	if _, err := f.Write(box[:]); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(len(b)) + padding); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	c.MaxOutputBytes = 128 << 20
	for _, path := range []string{fragmented, compact, large} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, supported, err := probeOwnedMP4(ctx, path, stat); err != nil || supported {
				t.Fatalf("unsupported shape admitted: %v/%v", supported, err)
			}
			_, duration, err := probe(ctx, c, path, false)
			if err != nil {
				t.Fatal(err)
			}
			wrapped := c
			commands := countMediaCommands(t, &wrapped)
			if _, err := verifyJoinedOwnedMP4(ctx, wrapped, path, request, duration, c.MaxOutputBytes, selection); err != nil {
				t.Fatal(err)
			}
			probes := 0
			for _, command := range commands() {
				if strings.HasPrefix(command, "ffprobe ") {
					probes++
				}
			}
			if probes != 1 {
				t.Fatalf("fallback is not freshly verified: %d", probes)
			}
			mcRun(t, ctx, c, "-xerror", "-i", path, "-f", "null", "-")
		})
	}
}
