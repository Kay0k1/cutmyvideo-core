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

func inspect(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("inspect", flag.ContinueOnError)
	f.SetOutput(stderr)
	input := f.String("input", "", "local input file")
	timeout := f.Duration("timeout", 2*time.Minute, "inspection timeout")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *input == "" || f.NArg() != 0 {
		return invalidCLI(errors.New("inspect requires --input and accepts named flags only"))
	}
	if *timeout <= 0 {
		return invalidCLI(errors.New("timeout must be positive"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	info, err := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), InspectTimeout: *timeout}).Inspect(ctx, *input)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(info)
}
