package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A stale result identity must not authorize a different inode, even when its
// bytes, size and timestamp are unchanged. File.Stat captures the opened file's
// identity before closing it, including on Windows where path Stat can be lazy.
func TestOwnedMP3FreshScanRejectsReplacedPathIdentity(t *testing.T) {
	c, ctx := mcConfig(t)
	path := ownedMP3Fixture(t, ctx, c, 44100, 1, 2000, "identity-original")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	before, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil {
		t.Fatal("capture opened output identity", statErr, closeErr)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, supported, err := probeOwnedMP3(ctx, path, before, ownedMP3DurationRaw); err != nil || !supported {
		t.Fatal("unchanged native output must enter bounded verification", err)
	}

	// Create both inodes before unlinking either one: the fixture cannot
	// accidentally reuse the original inode after deletion.
	replacement := filepath.Join(c.DataDir, "identity-replacement.mp3")
	if err := os.WriteFile(replacement, original, before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	file, err = os.Open(replacement)
	if err != nil {
		t.Fatal(err)
	}
	replaced, statErr := file.Stat()
	closeErr = file.Close()
	if statErr != nil || closeErr != nil {
		t.Fatal("capture replacement identity", statErr, closeErr)
	}
	if os.SameFile(before, replaced) || before.Size() != replaced.Size() || !before.ModTime().Equal(replaced.ModTime()) {
		t.Fatal("replacement must differ only in file identity")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, actual) {
		t.Fatal("replacement changed the valid native media bytes", err)
	}
	if _, _, supported, err := probeOwnedMP3(ctx, path, before, ownedMP3DurationRaw); err != nil || supported {
		t.Fatal("stale opened identity approved the replaced path", err)
	}
	// A fresh opened identity still validates the same native file. Rejection
	// above is specifically the ownership fence, not malformed media or mtime.
	file, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	current, statErr := file.Stat()
	closeErr = file.Close()
	if statErr != nil || closeErr != nil {
		t.Fatal("capture current opened identity", statErr, closeErr)
	}
	if _, _, supported, err := probeOwnedMP3(ctx, path, current, ownedMP3DurationRaw); err != nil || !supported {
		t.Fatal("valid replaced bytes failed their own fresh identity", err)
	}
}
