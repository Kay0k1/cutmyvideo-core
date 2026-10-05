//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run the CLI in a separate process: delivering a signal to the test runner
// would mask whether the CLI owns cancellation and reaps its media process.
func TestClipCancelsMediaOnSignal(t *testing.T) {
	if os.Getenv("CUTMY_CLI_SIGNAL_HELPER") == "1" {
		dir := os.Getenv("CUTMY_CLI_SIGNAL_DIR")
		err := clip([]string{"--input", filepath.Join(dir, "input.mp4"), "--output", filepath.Join(dir, "output.mp4"), "--end", "1"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("clip after signal: %v", err)
		}
		return
	}

	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "input.mp4"), []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			probe := filepath.Join(dir, "probe")
			pidFile := filepath.Join(dir, "probe.pid")
			// The shell replaces itself, so this PID is the process that must be
			// killed and reaped when the CLI receives the terminal signal.
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$$\" > %s\nexec sleep 30\n", shellQuote(pidFile))
			if err := os.WriteFile(probe, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClipCancelsMediaOnSignal$")
			cmd.Env = append(os.Environ(), "CUTMY_CLI_SIGNAL_HELPER=1", "CUTMY_CLI_SIGNAL_DIR="+dir, "FFPROBE_PATH="+probe)
			var log strings.Builder
			cmd.Stdout, cmd.Stderr = &log, &log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() { _ = cmd.Process.Kill() }()
			var pid int
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(pidFile)
				if err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
					if pid > 0 {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("probe did not start")
			}
			defer syscall.Kill(-pid, syscall.SIGKILL)
			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("CLI failed to cancel cleanly: %v\n%s", err, log.String())
				}
			case <-ctx.Done():
				t.Fatal("CLI did not stop after signal")
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("media process survived CLI cancellation: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "output.mp4")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled inspection created output: %v", err)
			}
		})
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
