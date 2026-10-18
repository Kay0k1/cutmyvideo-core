package main

import (
	"errors"
	"flag"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

type renderFlags struct {
	normalize                     bool
	lufs, peak, lra               float64
	canvas, fit, background       string
	replacement, replacementTrack string
	offset                        int64
}

func registerRenderFlags(f *flag.FlagSet) *renderFlags {
	r := &renderFlags{}
	f.BoolVar(&r.normalize, "audio-normalize", false, "explicitly normalize the completed output audio")
	f.Float64Var(&r.lufs, "normalize-lufs", -16, "integrated loudness target; requires --audio-normalize")
	f.Float64Var(&r.peak, "normalize-true-peak", -1, "true peak ceiling in dBTP; requires --audio-normalize")
	f.Float64Var(&r.lra, "normalize-lra", 11, "loudness range target in LU; requires --audio-normalize")
	f.StringVar(&r.canvas, "canvas", "", "contain the displayed picture in an even WIDTHxHEIGHT MP4 canvas")
	f.StringVar(&r.fit, "canvas-fit", "contain", "canvas fit mode: contain")
	f.StringVar(&r.background, "canvas-background", "#000000", "canvas padding color #RRGGBB")
	f.StringVar(&r.replacement, "audio-replace", "", "replace original audio with this regular local media file")
	f.StringVar(&r.replacementTrack, "replacement-audio-track", "default", "default or absolute audio stream index inside the replacement")
	f.Int64Var(&r.offset, "audio-offset-ms", 0, "replacement timestamp zero on source timeline, -86400000 through 86400000")
	return r
}

func (r *renderFlags) apply(f *flag.FlagSet, o *engine.AdvancedOptions) error {
	var orphan bool
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "normalize-lufs", "normalize-true-peak", "normalize-lra":
			orphan = orphan || !r.normalize
		case "canvas-fit", "canvas-background":
			orphan = orphan || r.canvas == ""
		case "replacement-audio-track", "audio-offset-ms":
			orphan = orphan || r.replacement == ""
		}
	})
	if orphan {
		return errors.New("render target, canvas and replacement modifiers require their explicit enabling option")
	}
	if r.normalize {
		o.AudioNormalize = &engine.AudioNormalize{IntegratedLUFS: &r.lufs, TruePeakDBTP: &r.peak, LoudnessRangeLU: &r.lra}
	}
	if r.canvas != "" {
		parts := strings.Split(r.canvas, "x")
		if len(parts) != 2 {
			return errors.New("canvas must be WIDTHxHEIGHT, for example 1080x1920")
		}
		width, err := strconv.Atoi(parts[0])
		if err != nil {
			return errors.New("canvas dimensions must be integers")
		}
		height, err := strconv.Atoi(parts[1])
		if err != nil {
			return errors.New("canvas dimensions must be integers")
		}
		o.Canvas = &engine.Canvas{Width: width, Height: height, Fit: r.fit, Background: r.background}
	}
	if r.replacement != "" {
		index, none, err := parseAudioSelection(r.replacementTrack)
		if err != nil || none {
			return errors.New("replacement-audio-track must be default or an inspected nonnegative audio index")
		}
		o.AudioReplace = &engine.AudioReplace{Input: r.replacement, AudioTrack: index, OffsetMS: r.offset}
	}
	return nil
}

func resolveReplacementInput(planPath string, r *engine.AudioReplace) error {
	if r == nil {
		return nil
	}
	if r.SourceID != "" {
		return invalidCLI(errors.New("local plans require audio_replace.input; HTTP source_id is unavailable"))
	}
	if r.Input != "" && !filepath.IsAbs(r.Input) {
		r.Input = filepath.Join(filepath.Dir(planPath), r.Input)
	}
	return nil
}
