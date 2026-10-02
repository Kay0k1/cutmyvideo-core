package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func clip(args []string) error {
	f := flag.NewFlagSet("clip", flag.ContinueOnError)
	input := f.String("input", "", "local input file")
	output := f.String("output", "", "output .mp4 or .mp3 file")
	start := f.String("start", "0", "start in seconds or HH:MM:SS.mmm")
	end := f.String("end", "", "end in seconds or HH:MM:SS.mmm")
	quality := f.String("quality", "best", "best, 1080p, or 720p")
	mode := f.String("mode", "accurate", "accurate or copy")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *input == "" || *output == "" || *end == "" {
		return errors.New("clip requires --input, --output, and --end")
	}
	startMS, err := parseTime(*start)
	if err != nil {
		return err
	}
	endMS, err := parseTime(*end)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result, err := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH")}).Export(ctx, *input, *output, engine.Range{StartMS: startMS, EndMS: endMS}, engine.Options{Quality: *quality, CutMode: *mode})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func parseTime(value string) (int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) > 3 || len(parts) == 0 {
		return 0, errors.New("invalid timestamp")
	}
	var total float64
	for i, part := range parts {
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
