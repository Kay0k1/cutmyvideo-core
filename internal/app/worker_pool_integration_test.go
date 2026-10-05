//go:build !windows

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWorkerPoolProcessesOnlyConfiguredSlotsAndReapsAllChildren(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	media := mediaConfig(t)
	c.WorkerConcurrency = 2
	c.FFprobe = media.FFprobe
	c.JobTimeout = time.Minute
	c.WorkerHealthPath = filepath.Join(t.TempDir(), "health")
	actual := filepath.Join(t.TempDir(), "fixture.mp4")
	if _, err := runCommand(context.Background(), media.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color=size=160x90:rate=25", "-t", "4", "-c:v", "libx264", "-threads", "1", actual); err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(actual)
	if err != nil {
		t.Fatal(err)
	}
	markers := t.TempDir()
	c.FFmpeg = filepath.Join(t.TempDir(), "encoder")
	// A sleeping child exposes slot ownership, then tests real Unix process-group
	// cancellation. The queue and both independent claims use PostgreSQL.
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$$\" > '%s/'\"$$\"\nexec sleep 30\n", markers)
	if err = os.WriteFile(c.FFmpeg, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.ConfigureStorage(c)
	for _, owner := range []string{"one", "two", "three"} {
		path := filepath.Join(c.DataDir, "sources", owner+".mp4")
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, input, 0600); err != nil {
			t.Fatal(err)
		}
		source := Source{ID: newID("src"), Owner: owner, Title: owner, Kind: "upload", Path: path, DurationMS: 4000, Width: 160, Height: 90}
		if err = s.AddSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
		if _, err = s.CreateJob(context.Background(), owner, requestFor(source), ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var workerErr error
	go func() { workerErr = RunWorker(ctx, c, s); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker cleanup did not stop")
		}
	})
	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(markers)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 2 {
			for _, entry := range entries {
				data, _ := os.ReadFile(filepath.Join(markers, entry.Name()))
				pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				pids = append(pids, pid)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pids) != 2 {
		t.Fatal("pool did not start two encoders", pids)
	}
	var running, queued int
	if err = s.DB.QueryRow(context.Background(), "SELECT count(*) FILTER(WHERE status='running'),count(*) FILTER(WHERE status='queued') FROM jobs").Scan(&running, &queued); err != nil {
		t.Fatal(err)
	}
	if running != 2 || queued != 1 {
		t.Fatalf("pool overclaimed: running=%d queued=%d", running, queued)
	}
	cancel()
	select {
	case <-done:
		if workerErr != nil {
			t.Fatal(workerErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pool did not stop")
	}
	for _, pid := range pids {
		if err = syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatal("encoder survived shutdown", pid, err)
		}
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 0 {
		t.Fatal("cleaned workspaces retained reservations", reserved)
	}
	if err = CheckWorkerHealth(c.WorkerHealthPath); err == nil {
		t.Fatal("stopped pool remained healthy")
	}
}
