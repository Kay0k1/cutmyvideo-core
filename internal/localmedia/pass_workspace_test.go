package localmedia

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPassWorkspaceBoundsAggregateAndRefusesUntrustedEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"video-0.log", "video-0.log.mbtree"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 60), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if checkPassWorkspace(dir, 100) == nil {
		t.Fatal("individual files bypassed aggregate statistics limit")
	}
	if err := checkPassWorkspace(dir, 120); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unexpected"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if checkPassWorkspace(dir, 200) == nil {
		t.Fatal("unknown file admitted into private statistics workspace")
	}
	if err := os.Remove(filepath.Join(dir, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "video-0.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "video-0.log"), 0700); err != nil {
		t.Fatal(err)
	}
	if checkPassWorkspace(dir, 200) == nil {
		t.Fatal("directory accepted as statistics file")
	}
}
