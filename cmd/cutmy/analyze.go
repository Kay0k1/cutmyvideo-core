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
	"path/filepath"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func runAnalyze(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: cutmy analyze silence|video|loudness --input recording.mp4 [--start 0 --end 30]\nPrints bounded source-timeline analysis as JSON; silence --plan prints an editable cutting plan without exporting media.")
		return err
	}
	kind := args[0]
	if kind != "silence" && kind != "video" && kind != "loudness" {
		return invalidCLI(errors.New("analyze requires silence, video or loudness"))
	}
	f := flag.NewFlagSet("analyze "+kind, flag.ContinueOnError)
	f.SetOutput(stderr)
	input := f.String("input", "", "regular local input file")
	start := f.String("start", "0", "source start in seconds or HH:MM:SS.mmm")
	end := f.String("end", "", "source end; empty uses finite source duration within the analysis limit")
	timeout := f.Duration("timeout", 2*time.Minute, "whole analysis deadline including inspection")
	threads := f.Int("threads", 2, "FFmpeg decoder threads, 1 through 32")
	var audio string
	if kind != "video" {
		f.StringVar(&audio, "audio-track", "default", "default or inspected absolute audio stream index")
	}
	var silence engine.SilenceOptions
	var video engine.VideoAnalysisOptions
	var asPlan bool
	var format, outputMode string
	if kind == "silence" {
		f.Float64Var(&silence.ThresholdDB, "threshold-db", -35, "strongest-channel 10 ms RMS threshold, -90 through -1 dBFS")
		f.Int64Var(&silence.MinSilenceMS, "min-silence-ms", 500, "minimum detected pause in milliseconds")
		f.Int64Var(&silence.KeepBeforeMS, "keep-before-ms", 0, "margin before audible material")
		f.Int64Var(&silence.KeepAfterMS, "keep-after-ms", 0, "margin after audible material")
		f.Int64Var(&silence.MinClipMS, "min-clip-ms", 0, "explicitly omit shorter suggested clips; zero retains all")
		f.BoolVar(&asPlan, "plan", false, "print an editable version 1 cutting plan instead of analysis")
		f.StringVar(&format, "format", "mp4", "plan output format: mp4 or mp3")
		f.StringVar(&outputMode, "output-mode", "joined", "plan output mode: joined or separate")
	}
	if kind == "video" {
		f.Float64Var(&video.SceneThreshold, "scene-threshold", 0.3, "visual scene discontinuity score threshold")
		f.Float64Var(&video.BlackPixelThreshold, "black-pixel-threshold", 0.1, "normalized luma threshold for dark pixels")
		f.Float64Var(&video.BlackPictureRatio, "black-picture-ratio", 0.98, "fraction of dark pixels needed for a black frame")
		f.Int64Var(&video.MinBlackMS, "min-black-ms", 500, "minimum black interval in milliseconds")
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *input == "" || f.NArg() != 0 || *timeout <= 0 || *threads < 1 || *threads > 32 {
		return invalidCLI(errors.New("analyze requires --input, named flags, a positive --timeout, and --threads 1..32"))
	}
	if kind == "silence" {
		var planOnlyFlag bool
		f.Visit(func(v *flag.Flag) {
			if v.Name == "format" || v.Name == "output-mode" {
				planOnlyFlag = true
			}
		})
		if planOnlyFlag && !asPlan {
			return invalidCLI(errors.New("--format and --output-mode require --plan"))
		}
		if asPlan && (format != "mp4" && format != "mp3" || outputMode != "joined" && outputMode != "separate") {
			return invalidCLI(errors.New("a silence plan requires --format mp4|mp3 and --output-mode joined|separate"))
		}
	}
	startMS, err := parseTime(*start)
	if err != nil {
		return invalidCLI(err)
	}
	var endMS int64
	if *end != "" {
		endMS, err = parseTime(*end)
		if err != nil {
			return invalidCLI(err)
		}
		if endMS <= startMS {
			return invalidCLI(errors.New("--end must be later than --start"))
		}
	}
	var audioTrack *int
	if kind != "video" {
		var noAudio bool
		audioTrack, noAudio, err = parseAudioSelection(audio)
		if err != nil || noAudio {
			return invalidCLI(errors.New("analysis audio-track must be default or a nonnegative inspected stream index"))
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	e := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), FFmpegThreads: *threads, InspectTimeout: *timeout, ExportTimeout: *timeout})
	var result any
	switch kind {
	case "silence":
		silence.StartMS, silence.EndMS, silence.AudioTrack = startMS, endMS, audioTrack
		analysis, analyzeErr := e.AnalyzeSilence(ctx, *input, silence)
		if analyzeErr != nil {
			return analyzeErr
		}
		result = analysis
		if asPlan {
			absolute, pathErr := filepath.Abs(*input)
			if pathErr != nil {
				return pathErr
			}
			result, err = engine.BuildSilencePlan(absolute, analysis, engine.AdvancedOptions{Options: engine.Options{Format: format, Quality: "best", CutMode: "accurate"}}, outputMode)
		}
	case "video":
		video.StartMS, video.EndMS = startMS, endMS
		result, err = e.AnalyzeVideo(ctx, *input, video)
	case "loudness":
		result, err = e.AnalyzeLoudness(ctx, *input, engine.LoudnessOptions{StartMS: startMS, EndMS: endMS, AudioTrack: audioTrack})
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
