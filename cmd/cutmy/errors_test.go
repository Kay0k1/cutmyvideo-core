package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func TestCLIJSONErrorsAndExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		status int
		code   engine.ErrorCode
	}{
		{[]string{"--json-errors", "unknown"}, 2, engine.CodeInvalidArgument},
		{[]string{"--json-errors", "clip", "--wat"}, 2, engine.CodeInvalidArgument},
		{[]string{"--json-errors", "inspect", "--input", filepath.Join(t.TempDir(), "absent")}, 1, engine.CodeInputNotFound},
		{[]string{"--json-errors", "clip", "--input", "unused", "--output", "unused.mp4", "--end", "1e3"}, 2, engine.CodeInvalidArgument},
		{[]string{"--json-errors", "server"}, 2, engine.CodeInvalidArgument},
	} {
		var stdout, stderr bytes.Buffer
		t.Setenv("DATABASE_URL", "")
		if status := runCLI(tc.args, &stdout, &stderr); status != tc.status {
			t.Fatalf("%v: status=%d; stderr=%s", tc.args, status, &stderr)
		}
		if stdout.Len() != 0 {
			t.Fatalf("failed CLI polluted stdout: %s", &stdout)
		}
		var response struct {
			Error struct {
				Code    engine.ErrorCode
				Message string
			}
		}
		if err := json.Unmarshal(stderr.Bytes(), &response); err != nil || response.Error.Code != tc.code || response.Error.Message == "" {
			t.Fatalf("expected one JSON envelope: %s, %v", &stderr, err)
		}
	}
	for _, pair := range []struct {
		err    error
		status int
	}{{context.Canceled, 130}, {context.DeadlineExceeded, 124}, {errors.New("unknown"), 1}, {&engine.Error{Code: "future_code"}, 1}} {
		if got := exitCode(engine.CodeOf(pair.err)); got != pair.status {
			t.Fatalf("error %v: status %d", pair.err, got)
		}
	}
	var stdout, stderr bytes.Buffer
	if status := runCLI([]string{"--json-errors", "version"}, &stdout, &stderr); status != 0 || stderr.Len() != 0 || !json.Valid(stdout.Bytes()) {
		t.Fatalf("successful JSON stdout changed: %d %s %s", status, &stdout, &stderr)
	}
	stdout.Reset()
	if status := runCLI([]string{"--json-errors", "clip", "--help"}, &stdout, &stderr); status != 0 || stdout.Len()+stderr.Len() == 0 {
		t.Fatal("JSON opt-in broke help")
	}
}

// Exercise actual process exit/stderr behavior without compiling a second CLI.
func TestCLIUnknownFlagJSONProcess(t *testing.T) {
	if os.Getenv("CUTMY_CLI_JSON_HELPER") == "1" {
		os.Exit(runCLI([]string{"--json-errors", "clip", "--unknown-option"}, os.Stdout, os.Stderr))
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIUnknownFlagJSONProcess$")
	cmd.Env = append(os.Environ(), "CUTMY_CLI_JSON_HELPER=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || stdout.Len() != 0 {
		t.Fatalf("CLI process: %v stdout=%s stderr=%s", err, &stdout, &stderr)
	}
	var body map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &body); err != nil || body["error"].(map[string]any)["code"] != "invalid_argument" {
		t.Fatalf("flag usage polluted JSON: %s %v", &stderr, err)
	}
}
