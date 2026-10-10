package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHDRCapabilityFailureKeepsProcessCause(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"zscale", "tonemap"} {
		t.Run(missing, func(t *testing.T) {
			// A child process models an installed FFmpeg without one required
			// filter; the configured real FFmpeg on this host has both filters.
			_, original := runCommand(context.Background(), executable, "-test.run=^TestHDRCapabilityDiagnosticFixture$", "--", "cutmy-hdr-diagnostic", missing)
			var failure *mediaProcessFailure
			var exit *exec.ExitError
			if !errors.As(original, &failure) || failure.category != "hdr_conversion_unavailable" || !errors.As(original, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("missing filter lost the actual process category/cause: %v", original)
			}
			wrapped := renderedMediaFailure(original, true)
			if LocalErrorCode(wrapped) != "hdr_conversion_unavailable" || exportProblem(wrapped).code != "hdr_conversion_unavailable" || !errors.Is(wrapped, original) {
				t.Fatalf("HDR conversion lost stable code or process cause: %v", wrapped)
			}
			var wrappedExit *exec.ExitError
			if !errors.As(wrapped, &wrappedExit) || wrappedExit != exit {
				t.Fatal("wrapped HDR failure changed the exit cause")
			}
			if strings.Contains(wrapped.Error(), "private-hdr-fixture") {
				t.Fatal("private subprocess diagnostic escaped")
			}
			if got := renderedMediaFailure(original, false); got != original {
				t.Fatalf("non-HDR operation changed its failure: %v", got)
			}
		})
	}
	for _, diagnostic := range []string{"scale", "invalid-option", "storage-full"} {
		t.Run(diagnostic, func(t *testing.T) {
			_, original := runCommand(context.Background(), executable, "-test.run=^TestHDRCapabilityDiagnosticFixture$", "--", "cutmy-hdr-diagnostic", diagnostic)
			if original == nil {
				t.Fatal("negative-control child unexpectedly succeeded")
			}
			if got := renderedMediaFailure(original, true); got != original {
				t.Fatalf("unrelated %s failure was relabeled as missing HDR capability: %v", diagnostic, got)
			}
		})
	}
}

func TestHDRFailurePreservesCancellationAndLimits(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errOutputLimit, errProcessResponseTooLarge} {
		t.Run(cause.Error(), func(t *testing.T) {
			original := fmt.Errorf("render: %w", cause)
			if got := renderedMediaFailure(original, true); got != original || !errors.Is(got, cause) {
				t.Fatalf("render wrapper lost unrelated cause %v: %v", cause, got)
			}
		})
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, original := runCommand(ctx, executable, "-test.run=^TestHDRCapabilityDiagnosticFixture$", "--", "cutmy-hdr-diagnostic", "zscale")
	if !errors.Is(original, context.Canceled) || renderedMediaFailure(original, true) != original {
		t.Fatalf("cancelled real process invocation lost cancellation: %v", original)
	}
	_, original = runCommand(context.Background(), executable, "-test.run=^TestProcessOutputFixture$", "--", "cutmy-output-overflow")
	if !errors.Is(original, errProcessResponseTooLarge) || renderedMediaFailure(original, true) != original {
		t.Fatalf("bounded child response became HDR failure: %v", original)
	}
}

func TestHDRCapabilityDiagnosticFixture(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "cutmy-hdr-diagnostic" || i+1 >= len(os.Args) {
			continue
		}
		switch kind := os.Args[i+1]; kind {
		case "zscale", "tonemap", "scale":
			fmt.Fprintf(os.Stderr, "No such filter: '%s' while processing private-hdr-fixture\n", kind)
		case "invalid-option":
			fmt.Fprintln(os.Stderr, "Error applying option 'npl' to filter 'zscale': Option not found")
		case "storage-full":
			fmt.Fprintln(os.Stderr, "No space left on device")
		default:
			os.Exit(2)
		}
		os.Exit(1)
	}
}
