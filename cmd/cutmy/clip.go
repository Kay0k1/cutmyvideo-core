package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func clip(args []string) error {
	return runClip(args, os.Stdout, os.Stderr)
}

func runClip(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("clip", flag.ContinueOnError)
	f.SetOutput(stderr)
	input := f.String("input", "", "local input file")
	output := f.String("output", "", "output .mp4/.mp3, .wav/.flac, or .mkv/.m4a (copy) file")
	start := f.String("start", "0", "start in seconds or HH:MM:SS.mmm")
	end := f.String("end", "", "end in seconds or HH:MM:SS.mmm")
	quality := f.String("quality", "best", "best, 1080p, or 720p")
	mode := f.String("mode", "accurate", "accurate or copy")
	audio := f.String("audio-track", "default", "default, none, or inspected absolute audio stream index")
	subtitles := f.String("subtitles", "omit", "omit or preserve compatible text subtitles")
	cropFlag := f.String("crop", "", "normalized upright crop x,y,width,height (accurate MP4 only)")
	render := registerRenderFlags(f)
	profile := f.String("profile", "fast", "fast (larger files) or compact")
	threads := f.Int("threads", 2, "FFmpeg threads, 1 through 32")
	timeout := f.Duration("timeout", 30*time.Minute, "whole export timeout, including inspection")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if f.NArg() != 0 {
		return invalidCLI(errors.New("clip accepts named flags only; use clip --help"))
	}
	if *input == "" || *output == "" || *end == "" {
		return invalidCLI(errors.New("clip requires --input, --output, and --end"))
	}
	if (*profile != "fast" && *profile != "compact") || *threads < 1 || *threads > 32 {
		return invalidCLI(errors.New("use --profile fast|compact and --threads 1..32"))
	}
	if *timeout <= 0 {
		return invalidCLI(errors.New("timeout must be positive"))
	}
	startMS, err := parseTime(*start)
	if err != nil {
		return invalidCLI(err)
	}
	endMS, err := parseTime(*end)
	if err != nil {
		return invalidCLI(err)
	}
	audioTrack, noAudio, err := parseAudioSelection(*audio)
	if err != nil {
		return invalidCLI(err)
	}
	crop, err := parseCrop(*cropFlag)
	if err != nil {
		return invalidCLI(err)
	}
	options := engine.AdvancedOptions{Options: engine.Options{Quality: *quality, CutMode: *mode}, AudioTrack: audioTrack, NoAudio: noAudio, SubtitleMode: *subtitles, Crop: crop}
	if err := render.apply(f, &options); err != nil {
		return invalidCLI(err)
	}
	// Media commands run in their own process group. Cancel their context before
	// exiting on a terminal signal so neither the encoder nor its children leak.
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, *timeout)
	defer cancel()
	result, err := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), EncodeProfile: *profile, FFmpegThreads: *threads, ExportTimeout: *timeout}).ExportWithOptions(ctx, *input, *output, engine.Range{StartMS: startMS, EndMS: endMS}, options)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func parseCrop(value string) (*engine.Crop, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return nil, errors.New("crop must be x,y,width,height in normalized displayed coordinates")
	}
	var values [4]float64
	for i, part := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errors.New("crop coordinates must be finite numbers")
		}
		values[i] = v
	}
	crop := &engine.Crop{X: values[0], Y: values[1], Width: values[2], Height: values[3]}
	if err := crop.Validate(); err != nil {
		return nil, err
	}
	return crop, nil
}

func parseAudioSelection(value string) (*int, bool, error) {
	switch value {
	case "default":
		return nil, false, nil
	case "none":
		return nil, true, nil
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 || index > 2147483647 {
		return nil, false, errors.New("audio-track must be default, none, or a nonnegative audio stream index from inspect")
	}
	return &index, false, nil
}

func parseTime(value string) (int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) > 3 || len(parts) == 0 {
		return 0, errors.New("invalid timestamp")
	}
	var total float64
	for i, part := range parts {
		// Only the final seconds component can be fractional. Reject signs,
		// exponents and fractional hours/minutes before numeric conversion.
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return 0, errors.New("invalid timestamp; use seconds or HH:MM:SS.mmm")
		}
		dots := 0
		for _, ch := range part {
			if ch == '.' && i == len(parts)-1 {
				dots++
			} else if ch < '0' || ch > '9' {
				return 0, errors.New("invalid timestamp; use seconds or HH:MM:SS.mmm")
			}
		}
		if dots > 1 {
			return 0, errors.New("invalid timestamp")
		}
		n, err := strconv.ParseFloat(part, 64)
		if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) || (i > 0 && n >= 60) {
			return 0, errors.New("invalid timestamp")
		}
		total = total*60 + n
	}
	if total > 30*24*3600 {
		return 0, errors.New("timestamp is too large")
	}
	return int64(math.Round(total * 1000)), nil
}
