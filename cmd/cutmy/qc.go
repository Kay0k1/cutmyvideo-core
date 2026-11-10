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

type qcExpectations []string

func (q *qcExpectations) String() string { return strings.Join(*q, ",") }
func (q *qcExpectations) Set(v string) error {
	*q = append(*q, strings.Split(v, ",")...)
	return nil
}

func runQC(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: cutmy qc --input final.mp4 [--expect fps=30,size=1080x1920,duration=14.7±0.1,true-peak=-1,cfr,no-drops] [--start 0 --end 30]\nMeasures the frames, picture, sound and container of a finished file and prints one JSON report.\nKeys: fps, size, duration, true-peak, lufs, cfr, no-drops, no-black, no-freeze, no-clipping, faststart, audio, no-audio, video.\nWhen an expectation fails the report is still printed and the command exits 1 with code expectation_failed.")
		return err
	}
	f := flag.NewFlagSet("qc", flag.ContinueOnError)
	f.SetOutput(stderr)
	var expect qcExpectations
	input := f.String("input", "", "regular local media file to measure")
	f.Var(&expect, "expect", "comma-separated key=value expectations; may be repeated")
	start := f.String("start", "0", "window start in seconds or HH:MM:SS.mmm")
	end := f.String("end", "", "window end; empty uses the duration within the 30 minute limit")
	minBlack := f.Int64("min-black-ms", 1, "shortest reported black interval in milliseconds")
	minFreeze := f.Int64("min-freeze-ms", 500, "shortest reported frozen interval in milliseconds")
	timeout := f.Duration("timeout", 2*time.Minute, "whole operation deadline including inspection")
	threads := f.Int("threads", 2, "FFmpeg decoder threads, 1 through 32")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *input == "" || f.NArg() != 0 || *timeout <= 0 || *threads < 1 || *threads > 32 {
		return invalidCLI(errors.New("qc requires --input, named flags, a positive --timeout, and --threads 1..32"))
	}
	if *minBlack < 1 || *minFreeze < 100 {
		return invalidCLI(errors.New("--min-black-ms must be at least 1 and --min-freeze-ms at least 100"))
	}
	options := engine.QCOptions{Expect: expect, MinBlackMS: *minBlack, MinFreezeMS: *minFreeze}
	var err error
	if options.StartMS, err = parseTime(*start); err != nil {
		return invalidCLI(err)
	}
	if *end != "" {
		if options.EndMS, err = parseTime(*end); err != nil {
			return invalidCLI(err)
		}
		if options.EndMS <= options.StartMS {
			return invalidCLI(errors.New("--end must be later than --start"))
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	e := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), FFmpegThreads: *threads, InspectTimeout: *timeout, ExportTimeout: *timeout})
	report, err := e.QC(ctx, *input, options)
	if err != nil && engine.CodeOf(err) != engine.CodeExpectationFailed {
		return err
	}
	// The report is the answer even when an expectation failed; the exit code
	// and the error carry the verdict.
	if encodeErr := json.NewEncoder(stdout).Encode(report); encodeErr != nil {
		return encodeErr
	}
	return err
}
