// Package toolrun executes external tools with bounded diagnostics and private
// process containment. Callers retain their own output and error policies.
package toolrun

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// Result preserves the raw process outcome for caller-owned diagnostics and
// error handling. Category is fixed vocabulary; it never contains stderr.
type Result struct {
	Elapsed    time.Duration
	ProcessErr error
	Category   string
}

// RunOutput streams stdout to the supplied writer and retains at most 16 KiB
// of stderr for classification. It does not normalize cancellation or replace
// the process error; callers decide how those outcomes affect their operation.
func RunOutput(ctx context.Context, path string, args []string, stdout io.Writer) Result {
	return RunOutputWithDiagnostics(ctx, path, args, stdout, nil)
}

// RunOutputWithDiagnostics additionally sends private stderr to a caller-owned
// bounded sink for machine-readable tool reports. Callers must never return
// this stream as an error or expose it to an HTTP/CLI response. Existing error
// classification and process containment are identical to RunOutput.
func RunOutputWithDiagnostics(ctx context.Context, path string, args []string, stdout, diagnostics io.Writer) Result {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = Environment()
	cmd.WaitDelay = 2 * time.Second
	stderr := &limitedBuffer{limit: 16 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if diagnostics != nil {
		cmd.Stderr = io.MultiWriter(stderr, diagnostics)
	}
	started := time.Now()
	processErr := runProcess(cmd)
	result := Result{Elapsed: time.Since(started), ProcessErr: processErr}
	if processErr != nil {
		result.Category = FailureCategory(stderr.String())
	}
	return result
}
