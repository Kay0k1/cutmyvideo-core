package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

// A saved plan is bounded independently from the media and is parsed strictly
// so misspelled settings cannot silently change a user's export.
func runBatch(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("batch", flag.ContinueOnError)
	f.SetOutput(stderr)
	planPath := f.String("plan", "", "saved version 1 JSON cutting plan")
	output := f.String("output", "", "existing directory for separate files, or joined output filename")
	profile := f.String("profile", "fast", "fast (larger files) or compact")
	threads := f.Int("threads", 2, "FFmpeg threads, 1 through 32")
	timeout := f.Duration("timeout", 30*time.Minute, "whole batch timeout, including inspection and publication")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *planPath == "" || *output == "" || f.NArg() != 0 {
		return invalidCLI(errors.New("batch requires --plan and --output and accepts named flags only"))
	}
	if (*profile != "fast" && *profile != "compact") || *threads < 1 || *threads > 32 || *timeout <= 0 {
		return invalidCLI(errors.New("use --profile fast|compact, --threads 1..32, and a positive timeout"))
	}
	file, err := os.Open(*planPath)
	if err != nil {
		return err
	}
	defer file.Close()
	const maxPlanBytes = 1 << 20
	blob, err := io.ReadAll(io.LimitReader(file, maxPlanBytes+1))
	if err != nil {
		return err
	}
	if len(blob) > maxPlanBytes {
		return invalidCLI(errors.New("plan exceeds 1 MiB; use at most 128 named intervals"))
	}
	var plan engine.Plan
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return invalidCLI(fmt.Errorf("invalid cutting plan: %w", err))
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return invalidCLI(errors.New("plan must contain exactly one JSON object"))
	}
	// Relative source paths resolve beside the saved plan, making the plan
	// portable and independent of the caller's current working directory.
	if plan.Input != "" && !filepath.IsAbs(plan.Input) {
		plan.Input = filepath.Join(filepath.Dir(*planPath), plan.Input)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), EncodeProfile: *profile, FFmpegThreads: *threads, ExportTimeout: *timeout}).ExecutePlan(ctx, plan, *output)
	if err != nil {
		if len(result.Outputs) > 0 {
			// A later interval can fail after earlier files were durably
			// published. Report those files without hiding the terminal error.
			if encodeErr := json.NewEncoder(stdout).Encode(struct {
				engine.BatchResult
				Incomplete bool `json:"incomplete"`
			}{result, true}); encodeErr != nil {
				return errors.Join(err, encodeErr)
			}
		}
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
