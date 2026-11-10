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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

// sheetList collects a repeatable flag.
type sheetList []string

func (l *sheetList) String() string     { return strings.Join(*l, ",") }
func (l *sheetList) Set(v string) error { *l = append(*l, v); return nil }

// sheetScenes is a flag with an optional value: bare --scenes detects scenes,
// --scenes=analysis.json reads a finished video analysis.
type sheetScenes struct {
	set  bool
	path string
}

func (s *sheetScenes) String() string   { return s.path }
func (s *sheetScenes) IsBoolFlag() bool { return true }
func (s *sheetScenes) Set(v string) error {
	s.set, s.path = v != "false", ""
	if v != "true" && v != "false" {
		s.path = v
	}
	return nil
}

// sheetArguments lets --images and --compare take several following values
// and --scenes take an optional one, which the flag package cannot express.
// File names beginning with '-' use the --flag=value form.
func sheetArguments(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		name := strings.TrimPrefix(strings.TrimPrefix(args[i], "-"), "-")
		if args[i] == "--" || !strings.HasPrefix(args[i], "-") || name != "images" && name != "compare" && name != "scenes" {
			out = append(out, args[i])
			if args[i] == "--" {
				return append(out, args[i+1:]...)
			}
			continue
		}
		values := 0
		for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && (name != "scenes" || values == 0) {
			i++
			values++
			out = append(out, "--"+name+"="+args[i])
		}
		if values == 0 {
			out = append(out, args[i])
		}
	}
	return out
}

// parseSheetInterval accepts a Go duration of whole milliseconds such as 2s
// or 500ms, or a plain timestamp in seconds, and returns milliseconds.
func parseSheetInterval(value string) (int64, error) {
	var ms int64
	if value != "" && (value[len(value)-1] < '0' || value[len(value)-1] > '9') {
		duration, err := time.ParseDuration(value)
		if err != nil || duration < time.Millisecond || duration%time.Millisecond != 0 || duration > 30*24*time.Hour {
			return 0, errors.New("invalid --every; use a duration such as 2s or 500ms, or seconds")
		}
		ms = duration.Milliseconds()
	} else {
		parsed, err := parseTime(value)
		if err != nil || parsed < 1 {
			return 0, errors.New("invalid --every; use a duration such as 2s or 500ms, or seconds")
		}
		ms = parsed
	}
	return ms, nil
}

// parseSheetRange accepts A-B; an omitted side means the source start or end.
func parseSheetRange(value string) (start, end int64, err error) {
	from, until, ok := strings.Cut(value, "-")
	if !ok || from == "" && until == "" {
		return 0, 0, errors.New("invalid --range; use A-B in seconds or HH:MM:SS.mmm")
	}
	if from != "" {
		if start, err = parseTime(from); err != nil {
			return 0, 0, err
		}
	}
	if until != "" {
		if end, err = parseTime(until); err != nil {
			return 0, 0, err
		}
		if end <= start {
			return 0, 0, errors.New("--range must end after it starts")
		}
	}
	return start, end, nil
}

func runSheet(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("sheet", flag.ContinueOnError)
	f.SetOutput(stderr)
	var inputs, images, compare sheetList
	var scenes sheetScenes
	f.Var(&inputs, "input", "local recording; repeat for several recordings on one sheet")
	f.Var(&images, "images", "JPEG/PNG files or directories for a sheet of images")
	f.Var(&compare, "compare", "two recordings: the same moments side by side with luma SSIM")
	output := f.String("output", "", "new .jpg/.jpeg or .png sheet; several pages add -001, -002, ...; existing files are preserved")
	interval := f.String("range", "", "source interval A-B, seconds or HH:MM:SS.mmm; default whole source")
	every := f.String("every", "", "one frame per interval, such as 2s, 500ms or 2")
	count := f.Int("count", 0, "frames at the middles of equal parts of the range (default 24)")
	at := f.String("at", "", "comma-separated source times, seconds or HH:MM:SS.mmm")
	f.Var(&scenes, "scenes", "one frame per scene; an optional value names an analyze video JSON result")
	columns := f.Int("cols", 8, "tiles per row, 1 through 64")
	tile := f.Int("tile-width", 320, "tile width in pixels, 48 through 1920")
	rows := f.Int("max-rows", 6, "rows per page, 1 through 64; more frames add pages")
	label := f.String("label", "", "time, index, name in drawing order, or none (default time; name for images)")
	format := f.String("format", "", "jpeg or png; default follows the output extension")
	threads := f.String("threads", "auto", "auto (CPUs, at most 16) or 1 through 32 parallel decoders")
	timeout := f.Duration("timeout", 2*time.Minute, "whole sheet deadline including inspection")
	threshold := f.Float64("ssim-threshold", 0.95, "comparison pairs below this SSIM are reported and marked")
	if err := f.Parse(sheetArguments(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	given := map[string]bool{}
	f.Visit(func(v *flag.Flag) { given[v.Name] = true })
	modes := 0
	for _, list := range []sheetList{inputs, images, compare} {
		if len(list) != 0 {
			modes++
		}
	}
	if f.NArg() != 0 || modes != 1 || *output == "" || *timeout <= 0 {
		return invalidCLI(errors.New("sheet requires --output, a positive --timeout and one of --input, --images or --compare with named flags"))
	}
	if len(compare) != 0 && len(compare) != 2 {
		return invalidCLI(errors.New("--compare requires exactly two recordings"))
	}
	// Zero means "default" to the engine; an explicit flag must be in range.
	if *columns < 1 || *columns > 64 || *rows < 1 || *rows > 64 || *tile < 48 || *tile > 1920 || *threshold <= 0 || *threshold > 1 {
		return invalidCLI(errors.New("sheet requires --cols 1..64, --max-rows 1..64, --tile-width 48..1920 and --ssim-threshold above 0 through 1"))
	}
	options := engine.SheetOptions{Inputs: inputs, Images: images, Columns: *columns, TileWidth: *tile, MaxRows: *rows, Format: *format}
	if len(compare) != 0 {
		options.Inputs, options.Compare, options.SSIMThreshold = compare, true, *threshold
	} else if given["ssim-threshold"] {
		return invalidCLI(errors.New("--ssim-threshold requires --compare"))
	}
	selections := 0
	for _, name := range []string{"every", "count", "at", "scenes"} {
		if given[name] {
			selections++
		}
	}
	if selections > 1 || given["scenes"] && !scenes.set {
		return invalidCLI(errors.New("choose one of --every, --count, --at and --scenes"))
	}
	var err error
	if given["range"] {
		if options.StartMS, options.EndMS, err = parseSheetRange(*interval); err != nil {
			return invalidCLI(err)
		}
	}
	if given["every"] {
		if options.EveryMS, err = parseSheetInterval(*every); err != nil {
			return invalidCLI(err)
		}
	}
	if given["count"] {
		if *count < 1 {
			return invalidCLI(errors.New("--count must be positive"))
		}
		options.Count = *count
	}
	if given["at"] {
		for _, value := range strings.Split(*at, ",") {
			timestamp, timeErr := parseTime(value)
			if timeErr != nil {
				return invalidCLI(timeErr)
			}
			options.AtMS = append(options.AtMS, timestamp)
		}
	}
	if scenes.set {
		options.Scenes = true
		if scenes.path != "" {
			data, readErr := os.ReadFile(scenes.path)
			if readErr != nil {
				return readErr
			}
			options.SceneAnalysis = &engine.VideoAnalysisResult{}
			if json.Unmarshal(data, options.SceneAnalysis) != nil {
				return invalidCLI(errors.New("--scenes file must be a cutmy analyze video JSON result"))
			}
		}
	}
	if given["label"] {
		options.Labels = strings.Split(*label, ",")
	}
	if *threads != "auto" {
		if options.Threads, err = strconv.Atoi(*threads); err != nil || options.Threads < 1 || options.Threads > 32 {
			return invalidCLI(fmt.Errorf("--threads must be auto or 1..32"))
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	result, err := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), InspectTimeout: *timeout, ExportTimeout: *timeout}).GenerateSheet(ctx, *output, options)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
