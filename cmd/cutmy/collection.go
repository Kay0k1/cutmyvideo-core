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
	"strings"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func runCollection(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := fmt.Fprintln(stdout, "Usage: cutmy collection validate|export --plan collection.json --output ./clips [--clips id1,id2] [--rendition final|draft]")
		return err
	}
	if args[0] != "validate" && args[0] != "export" {
		return invalidCLI(errors.New("collection requires validate or export"))
	}
	command := args[0]
	f := flag.NewFlagSet("collection "+command, flag.ContinueOnError)
	f.SetOutput(stderr)
	planPath := f.String("plan", "", "saved version 1 JSON collection plan")
	output := f.String("output", "", "existing output directory; existing results are preserved")
	ids := f.String("clips", "", "comma-separated stable clip IDs; empty means all, in plan order")
	rendition := f.String("rendition", "final", "final or draft (accurate MP4, at most 720p, lower encode quality)")
	profile := f.String("profile", "fast", "fast or compact final encode profile")
	threads := f.Int("threads", 2, "FFmpeg threads, 1 through 32")
	defaultTimeout := 30 * time.Minute
	if command == "validate" {
		defaultTimeout = 2 * time.Minute
	}
	timeout := f.Duration("timeout", defaultTimeout, "whole operation timeout, including inspection and publication")
	maxBytes := f.Int64("max-output-bytes", 10<<30, "aggregate selected collection output byte limit")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *planPath == "" || *output == "" || f.NArg() != 0 {
		return invalidCLI(errors.New("collection requires --plan and --output and accepts named flags only"))
	}
	if (*profile != "fast" && *profile != "compact") || *threads < 1 || *threads > 32 || *timeout <= 0 || *maxBytes <= 0 || (*rendition != "final" && *rendition != "draft") {
		return invalidCLI(errors.New("use --profile fast|compact, --threads 1..32, --rendition final|draft, and positive timeout/output byte limit"))
	}
	plan, err := readCollectionPlan(*planPath)
	if err != nil {
		return err
	}
	execution := engine.CollectionExecution{Rendition: *rendition}
	if *ids != "" {
		for _, id := range strings.Split(*ids, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				return invalidCLI(errors.New("--clips must contain nonempty comma-separated clip IDs"))
			}
			execution.ClipIDs = append(execution.ClipIDs, id)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config := engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), EncodeProfile: *profile, FFmpegThreads: *threads, MaxOutputBytes: *maxBytes}
	if command == "validate" {
		config.InspectTimeout = *timeout
		result, err := engine.New(config).PreflightCollection(ctx, plan, *output, execution)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	}
	config.ExportTimeout = *timeout
	result, err := engine.New(config).ExecuteCollection(ctx, plan, *output, execution)
	if err != nil {
		if len(result.Outputs) > 0 {
			if encodeErr := json.NewEncoder(stdout).Encode(struct {
				engine.CollectionResult
				Incomplete bool `json:"incomplete"`
			}{result, true}); encodeErr != nil {
				return errors.Join(err, encodeErr)
			}
		}
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func readCollectionPlan(path string) (plan engine.CollectionPlan, err error) {
	file, err := os.Open(path)
	if err != nil {
		return plan, err
	}
	defer file.Close()
	const maxCollectionBytes = 1 << 20
	blob, err := io.ReadAll(io.LimitReader(file, maxCollectionBytes+1))
	if err != nil {
		return plan, err
	}
	if len(blob) > maxCollectionBytes {
		return plan, invalidCLI(errors.New("collection plan exceeds 1 MiB"))
	}
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return plan, invalidCLI(fmt.Errorf("invalid collection plan: %w", err))
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return plan, invalidCLI(errors.New("collection plan must contain exactly one JSON object"))
	}
	if plan.Input != "" && !filepath.IsAbs(plan.Input) {
		plan.Input = filepath.Join(filepath.Dir(path), plan.Input)
	}
	if err := resolveReplacementInput(path, plan.Options.AudioReplace); err != nil {
		return plan, err
	}
	for i := range plan.Clips {
		if err := resolveReplacementInput(path, plan.Clips[i].Options.AudioReplace); err != nil {
			return plan, err
		}
	}
	return plan, nil
}
