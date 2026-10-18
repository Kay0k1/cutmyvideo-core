package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func runPoster(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("poster", flag.ContinueOnError)
	f.SetOutput(stderr)
	input := f.String("input", "", "verified completed local video")
	output := f.String("output", "", "new .jpg/.jpeg or .png output; existing files are preserved")
	at := f.String("at", "", "required timestamp on the input video's output clock, seconds or HH:MM:SS.mmm")
	format := f.String("format", "jpeg", "jpeg or png; must match the output extension")
	threads := f.Int("threads", 2, "decoder threads, 1 through 32")
	timeout := f.Duration("timeout", 30*time.Second, "whole poster deadline including inspection")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if f.NArg() != 0 || *input == "" || *output == "" || *at == "" || *timeout <= 0 || *threads < 1 || *threads > 32 || (*format != "jpeg" && *format != "png") {
		return invalidCLI(errors.New("poster requires --input, --output, --at, jpeg|png, positive --timeout and --threads 1..32"))
	}
	timestamp, err := parseTime(*at)
	if err != nil {
		return invalidCLI(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	result, err := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), FFmpegThreads: *threads, ExportTimeout: *timeout}).GeneratePoster(ctx, *input, *output, engine.PosterOptions{TimestampMS: timestamp, Format: *format})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
