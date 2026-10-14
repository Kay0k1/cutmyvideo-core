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

func runPreflight(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("preflight", flag.ContinueOnError)
	f.SetOutput(stderr)
	planPath := f.String("plan", "", "saved version 1 JSON cutting plan")
	output := f.String("output", "", "proposed directory for separate files, or joined output filename")
	profile := f.String("profile", "fast", "fast or compact")
	threads := f.Int("threads", 2, "FFmpeg threads, 1 through 32")
	timeout := f.Duration("timeout", 2*time.Minute, "inspection and capability query timeout")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *planPath == "" || *output == "" || f.NArg() != 0 {
		return invalidCLI(errors.New("preflight requires --plan and --output and accepts named flags only"))
	}
	if (*profile != "fast" && *profile != "compact") || *threads < 1 || *threads > 32 || *timeout <= 0 {
		return invalidCLI(errors.New("use --profile fast|compact, --threads 1..32, and a positive timeout"))
	}
	plan, err := readCuttingPlan(*planPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"),
		EncodeProfile: *profile, FFmpegThreads: *threads, InspectTimeout: *timeout}).PreflightPlan(ctx, plan, *output)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
