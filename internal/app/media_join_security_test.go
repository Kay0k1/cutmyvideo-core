package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// The trusted concat manifest needs file/concat enabled to open itself. Each
// media entry must keep the original restrictions even if its upstream changes
// after selection. This uses the real probe/decoder and observable private I/O,
// rather than treating an expected FFmpeg command line as a security oracle.
func TestJoinedRemoteEntryKeepsMediaFormatAndProtocolRestrictions(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := filepath.Join(c.DataDir, "source.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=4:size=160x90:rate=25", "-c:v", "libx264", "-threads", "1", "-movflags", "+faststart", fixture); err != nil {
		t.Fatal(err)
	}
	media, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var malicious atomic.Bool
	var privateHits atomic.Int64
	var payload atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/private" {
			privateHits.Add(1)
			http.ServeContent(w, r, "private.mp4", time.Time{}, bytes.NewReader(media))
			return
		}
		if malicious.Load() {
			http.ServeContent(w, r, "changed.mp4", time.Time{}, bytes.NewReader(payload.Load().([]byte)))
			return
		}
		http.ServeContent(w, r, "source.mp4", time.Time{}, bytes.NewReader(media))
	}))
	defer server.Close()
	forbidden := filepath.Join(c.DataDir, "forbidden-local.ffconcat")
	if err := os.WriteFile(forbidden, []byte(fmt.Sprintf("ffconcat version 1.0\nfile '%s/private'\n", server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	inputs := []mediaInput{{Path: server.URL + "/source", Remote: true}}
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined"}
	selection, err := probeInputSelectionOptions(ctx, c, inputs, request)
	if err != nil || selection.Video == nil {
		t.Fatal("valid upstream did not pass inspection", err)
	}
	ranges := []Range{{StartMS: 0, EndMS: 1000}, {StartMS: 2000, EndMS: 3000}}
	workspace := t.TempDir()
	out := filepath.Join(workspace, "valid.mp4")
	if _, err = joinAccurate(ctx, c, [][]mediaInput{inputs, inputs}, ranges, request, out, workspace, c.MaxOutputBytes, selection, nil); err != nil {
		t.Fatal("protected remote media no longer works", err)
	}
	if _, err = runCommand(ctx, c.FFmpeg, "-v", "error", "-xerror", "-i", out, "-f", "null", "-"); err != nil {
		t.Fatal("normal joined result failed independent decode", err)
	}
	for _, nested := range []string{"private", server.URL + "/private", filepath.ToSlash(forbidden)} {
		t.Run(nested, func(t *testing.T) {
			privateHits.Store(0)
			payload.Store([]byte(fmt.Sprintf("ffconcat version 1.0\nfile '%s'\n", nested)))
			malicious.Store(true)
			workspace := t.TempDir()
			_, err := joinAccurate(ctx, c, [][]mediaInput{inputs, inputs}, ranges, request, filepath.Join(workspace, "changed.mp4"), workspace, c.MaxOutputBytes, selection, nil)
			if err == nil {
				t.Fatal("changed upstream escaped the per-entry media whitelist")
			}
			if privateHits.Load() != 0 {
				t.Fatal("upstream concat reached an unguarded private endpoint")
			}
		})
	}
}
