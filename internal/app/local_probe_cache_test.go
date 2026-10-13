package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalProbeCacheReusesVerifiedFileAndInvalidatesChanges(t *testing.T) {
	c := mediaConfig(t)
	path := previewFixture(t, c)
	commands := countMediaCommands(t, &c)
	store := &Store{}
	source := Source{ID: "source", Owner: "owner", Path: path}
	ctx, session, hit := store.beginCachedLocal(context.Background(), c, source)
	defer session.Close()
	if hit {
		t.Fatal("cold source reported a hit")
	}
	if _, err := InspectLocal(ctx, c, path); err != nil {
		t.Fatal(err)
	}
	store.rememberLocalProbe(ctx, c, source)
	check := func(source Source, config Config, want bool) {
		t.Helper()
		cached, session, hit := store.beginCachedLocal(context.Background(), config, source)
		defer session.Close()
		if hit != want {
			t.Fatalf("cache hit=%v, want %v", hit, want)
		}
		if hit {
			if _, err := InspectLocal(cached, config, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	check(source, c, true)
	if len(commands()) != 1 {
		t.Fatalf("warm inspection reran ffprobe: %v", commands())
	}
	foreign := source
	foreign.Owner = "other"
	check(foreign, c, false)
	changedTool := c
	changedTool.FFprobe = "other-tool"
	check(source, changedTool, false)
	store.rememberLocalProbe(ctx, c, source)
	entry := store.localProbes.entries[localProbeKey(source)]
	entry.expires = time.Now().Add(-time.Second)
	store.localProbes.entries[localProbeKey(source)] = entry
	check(source, c, false)
	store.rememberLocalProbe(ctx, c, source)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(c.DataDir, "replacement.mkv")
	if err := os.WriteFile(replacement, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	check(source, c, false)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check(source, c, false)
}

func TestLocalProbeCacheBoundsMetadataAndDoesNotReuseOutputProbes(t *testing.T) {
	c := mediaConfig(t)
	path := previewFixture(t, c)
	commands := countMediaCommands(t, &c)
	store := &Store{}
	source := Source{ID: "source", Owner: "owner", Path: path}
	ctx, session, _ := store.beginCachedLocal(context.Background(), c, source)
	defer session.Close()
	if _, err := InspectLocal(ctx, c, path); err != nil {
		t.Fatal(err)
	}
	for i := range localProbeCacheEntries + 5 {
		source.ID = fmt.Sprint(i)
		store.rememberLocalProbe(ctx, c, source)
	}
	if len(store.localProbes.entries) != localProbeCacheEntries {
		t.Fatal("unbounded metadata cache")
	}
	output := filepath.Join(c.DataDir, "preview.mp4")
	if err := renderPreview(ctx, c, []mediaInput{{Path: path}}, Range{StartMS: 0, EndMS: 2000}, output); err != nil {
		t.Fatal(err)
	}
	var inputs, outputs int
	for _, command := range commands() {
		if strings.HasPrefix(command, "ffprobe") {
			if strings.Contains(command, path) {
				inputs++
			}
			if strings.Contains(command, output) {
				outputs++
			}
		}
	}
	if inputs != 1 || outputs != 1 {
		t.Fatalf("source/output probe counts %d/%d", inputs, outputs)
	}
}
