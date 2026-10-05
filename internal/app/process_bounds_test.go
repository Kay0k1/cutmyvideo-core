package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCopyBoundedPreservesBoundaryAndRejectsOverflow(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		limit             int64
		tooLarge          bool
	}{
		{"empty", "", "", 0, false},
		{"zero", "x", "", 0, true},
		{"exact", "1234", "1234", 4, false},
		{"over", "12345", "1234", 4, true},
		{"short", "123", "123", 4, false},
		{"largest", "123", "123", math.MaxInt64, false},
		{"negative", "123", "", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			err := copyBounded(&dst, strings.NewReader(tc.input), tc.limit)
			if errors.Is(err, errSourceTooLarge) != tc.tooLarge || (err != nil && !tc.tooLarge) || dst.String() != tc.want {
				t.Fatalf("copied=%q err=%v", dst.String(), err)
			}
		})
	}
}

func TestProcessOutputStopsUnboundedProducer(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	_, err = runCommand(ctx, path, "-test.run=^TestProcessOutputFixture$", "--", "cutmy-output-overflow")
	if !errors.Is(err, errProcessResponseTooLarge) || time.Since(started) >= 4*time.Second {
		t.Fatalf("unbounded producer was not interrupted: %v", err)
	}
}

func TestProcessOutputAcceptsExactLimit(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := runCommand(ctx, path, "-test.run=^TestProcessOutputFixture$", "--", "cutmy-output-exact")
	if err != nil || len(data) != 8<<20 {
		t.Fatalf("exact response rejected: size=%d err=%v", len(data), err)
	}
}

func TestProcessOutputFixture(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "cutmy-output-exact" {
			_, _ = io.CopyN(os.Stdout, strings.NewReader(strings.Repeat("x", 8<<20)), 8<<20)
			os.Exit(0)
		}
		if arg == "cutmy-output-overflow" {
			block := []byte(strings.Repeat("x", 32<<10))
			for {
				if _, err := os.Stdout.Write(block); err != nil {
					os.Exit(0)
				}
			}
		}
	}
}

func TestDiagnosticCopyCannotBypassMemoryBound(t *testing.T) {
	dst := &limitedBuffer{limit: 16 << 10}
	input := strings.Repeat("private fixture", 10000)
	n, err := io.Copy(dst, io.NopCloser(strings.NewReader(input)))
	if err != nil || n != int64(len(input)) || len(dst.String()) != 16<<10 {
		t.Fatalf("diagnostic limit bypassed: read=%d retained=%d err=%v", n, len(dst.String()), err)
	}
}
