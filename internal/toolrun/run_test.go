package toolrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunOutputPreservesRawFailureAndStreaming(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	result := RunOutput(ctx, executable,
		[]string{"-test.run=^TestRunOutputFixture$", "--", "cutmy-toolrun-fixture", "failure"}, &stdout)
	var exit *exec.ExitError
	if !errors.As(result.ProcessErr, &exit) || exit.ExitCode() != 7 || result.Category != "network_timeout" {
		t.Fatalf("raw process outcome changed: %+v", result)
	}
	if stdout.String() != "streamed prefix\n" || result.Elapsed <= 0 {
		t.Fatalf("process output or execution observation lost: stdout=%q result=%+v", stdout.String(), result)
	}
	if strings.Contains(result.ProcessErr.Error(), "private-toolrun-fixture") || len(exit.Stderr) != 0 {
		t.Fatal("private stderr escaped through the process cause", result.ProcessErr)
	}
	var streamed countedOutput
	result = RunOutput(ctx, executable,
		[]string{"-test.run=^TestRunOutputFixture$", "--", "cutmy-toolrun-fixture", "stream"}, &streamed)
	if result.ProcessErr != nil || result.Category != "" || streamed != 9<<20 {
		t.Fatalf("streaming inherited the caller's byte-response bound: bytes=%d result=%+v", streamed, result)
	}
	result = RunOutput(ctx, filepath.Join(t.TempDir(), "missing-tool"), nil, &stdout)
	if result.ProcessErr == nil || (!errors.Is(result.ProcessErr, os.ErrNotExist) && !errors.Is(result.ProcessErr, exec.ErrNotFound)) || result.Category != "unknown" {
		t.Fatalf("missing tool lost its original OS cause: %+v", result)
	}
}

type countedOutput int64

func (w *countedOutput) Write(data []byte) (int, error) {
	*w += countedOutput(len(data))
	return len(data), nil
}

type readyOutput struct {
	buffer bytes.Buffer
	ready  chan struct{}
}

func (w *readyOutput) Write(data []byte) (int, error) {
	n, err := w.buffer.Write(data)
	if bytes.Contains(w.buffer.Bytes(), []byte("ready\n")) {
		select {
		case <-w.ready:
		default:
			close(w.ready)
		}
	}
	return n, err
}

func TestRunOutputPreservesCancellation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := RunOutput(ctx, executable, nil, nil)
	if !errors.Is(result.ProcessErr, context.Canceled) {
		t.Fatalf("pre-start cancellation lost its raw cause: %+v", result)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stdout := &readyOutput{ready: make(chan struct{})}
	done := make(chan Result, 1)
	go func() {
		done <- RunOutput(ctx, executable,
			[]string{"-test.run=^TestRunOutputFixture$", "--", "cutmy-toolrun-fixture", "wait"}, stdout)
	}()
	select {
	case <-stdout.ready:
	case result := <-done:
		t.Fatalf("process stopped before readiness: %+v", result)
	case <-time.After(6 * time.Second):
		t.Fatal("process did not become ready")
	}
	cancel()
	select {
	case result := <-done:
		var exit *exec.ExitError
		if !errors.As(result.ProcessErr, &exit) || result.Category != "unknown" || result.Elapsed <= 0 {
			t.Fatalf("running process lost its raw cancellation outcome: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled process did not finish")
	}
}

func TestRunOutputFixture(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "cutmy-toolrun-fixture" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "failure":
		fmt.Fprintln(os.Stdout, "streamed prefix")
		fmt.Fprintln(os.Stderr, "Connection timed out: https://private-toolrun-fixture.invalid/?token=private-toolrun-fixture")
		os.Exit(7)
	case "stream":
		block := []byte(strings.Repeat("x", 32<<10))
		for range 288 {
			if _, err := os.Stdout.Write(block); err != nil {
				os.Exit(3)
			}
		}
		os.Exit(0)
	case "wait":
		fmt.Fprintln(os.Stdout, "ready")
		for {
			time.Sleep(time.Hour)
		}
	}
}
