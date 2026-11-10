package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

type syncInputs []string

func (s *syncInputs) String() string     { return strings.Join(*s, ",") }
func (s *syncInputs) Set(v string) error { *s = append(*s, v); return nil }

func runSync(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: cutmy sync --ref track.wav --input clip.mp4 [--input take2.mp4] [--band auto|full|vocal|drums] [--all]\nPrints where the reference is heard in each input as JSON: input_ms = ref_ms + offset_ms. Exits 1 with code no_match when an input has no single confident placement.")
		return err
	}
	f := flag.NewFlagSet("sync", flag.ContinueOnError)
	f.SetOutput(stderr)
	var inputs syncInputs
	ref := f.String("ref", "", "reference recording, usually the finished track")
	f.Var(&inputs, "input", "recording to place against the reference; repeat for up to 16 inputs")
	band := f.String("band", "auto", "auto, full, vocal (a remix that keeps the vocal) or drums")
	all := f.Bool("all", false, "list every placement nearly as strong as the best one")
	refStart := f.String("ref-start", "0", "reference window start in seconds or HH:MM:SS.mmm")
	refEnd := f.String("ref-end", "", "reference window end; empty uses its duration within the 30 minute limit")
	start := f.String("start", "0", "window start applied to every input")
	end := f.String("end", "", "window end applied to every input")
	timeout := f.Duration("timeout", 2*time.Minute, "whole operation deadline including inspection")
	threads := f.Int("threads", 4, "worker threads, 1 through 32")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *ref == "" || len(inputs) == 0 || f.NArg() != 0 || *timeout <= 0 || *threads < 1 || *threads > 32 {
		return invalidCLI(errors.New("sync requires --ref, at least one --input, named flags, a positive --timeout, and --threads 1..32"))
	}
	options := engine.SyncOptions{Band: *band, All: *all}
	var err error
	if options.RefStartMS, options.RefEndMS, err = parseWindow(*refStart, *refEnd, "--ref-end must be later than --ref-start"); err != nil {
		return invalidCLI(err)
	}
	if options.StartMS, options.EndMS, err = parseWindow(*start, *end, "--end must be later than --start"); err != nil {
		return invalidCLI(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	e := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), FFmpegThreads: *threads, InspectTimeout: *timeout, ExportTimeout: *timeout})
	result, err := e.SyncAudio(ctx, *ref, inputs, options)
	if err != nil && engine.CodeOf(err) != engine.CodeNoMatch {
		return err
	}
	// The per-input report is the answer even when some input did not match.
	if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}

func parseWindow(start, end, message string) (startMS, endMS int64, err error) {
	if startMS, err = parseTime(start); err != nil {
		return 0, 0, err
	}
	if end == "" {
		return startMS, 0, nil
	}
	if endMS, err = parseTime(end); err != nil {
		return 0, 0, err
	}
	if endMS <= startMS {
		return 0, 0, errors.New(message)
	}
	return startMS, endMS, nil
}
