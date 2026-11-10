package mediaqc

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxExpectations bounds one request.
const MaxExpectations = 32

// Expectation is one validated key=value requirement from ParseExpectations.
type Expectation struct {
	key           string
	value, spread float64
	width, height int
}

type keySpec struct {
	value  bool    // requires a value
	spread float64 // default ± tolerance; negative forbids one
	low    float64
	high   float64
}

var keys = map[string]keySpec{
	"fps":         {value: true, spread: 0.01, low: 0.001, high: 1000},
	"size":        {value: true},
	"duration":    {value: true, spread: 0.05, low: 0.001, high: 30 * 24 * 3600},
	"true-peak":   {value: true, spread: -1, low: -100, high: 20},
	"lufs":        {value: true, spread: 1, low: -100, high: 0},
	"cfr":         {},
	"no-drops":    {},
	"no-black":    {},
	"no-freeze":   {},
	"no-clipping": {},
	"faststart":   {},
	"audio":       {},
	"no-audio":    {},
	"video":       {},
}

// ParseExpectations validates items such as "fps=60", "size=1080x1920",
// "duration=14.7±0.1" ("+-" is accepted for "±"), "true-peak=-1", "lufs=-14"
// and the flags cfr, no-drops, no-black, no-freeze, no-clipping, faststart,
// audio, no-audio and video. Each key may appear once.
func ParseExpectations(items []string) ([]Expectation, error) {
	if len(items) > MaxExpectations {
		return nil, fmt.Errorf("at most %d expectations are accepted", MaxExpectations)
	}
	result := make([]Expectation, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		key, value, hasValue := strings.Cut(strings.TrimSpace(item), "=")
		spec, known := keys[key]
		if !known {
			return nil, fmt.Errorf("unknown expectation %q", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("expectation %q is repeated", key)
		}
		seen[key] = true
		if hasValue != spec.value {
			if spec.value {
				return nil, fmt.Errorf("expectation %q requires a value", key)
			}
			return nil, fmt.Errorf("expectation %q does not take a value", key)
		}
		e := Expectation{key: key}
		var err error
		switch {
		case key == "size":
			e.width, e.height, err = parseSize(value)
		case spec.value:
			e.value, e.spread, err = parseNumber(value, spec)
		}
		if err != nil {
			return nil, fmt.Errorf("expectation %q: %w", key, err)
		}
		result = append(result, e)
	}
	if seen["audio"] && seen["no-audio"] {
		return nil, errors.New("expectations audio and no-audio contradict each other")
	}
	return result, nil
}

func parseSize(value string) (int, int, error) {
	w, h, ok := strings.Cut(value, "x")
	width, widthErr := strconv.Atoi(w)
	height, heightErr := strconv.Atoi(h)
	if !ok || widthErr != nil || heightErr != nil || width < 1 || height < 1 || width > 65536 || height > 65536 {
		return 0, 0, errors.New("use WIDTHxHEIGHT in display pixels, for example 1080x1920")
	}
	return width, height, nil
}

func parseNumber(value string, spec keySpec) (float64, float64, error) {
	number, spreadText, hasSpread := strings.Cut(value, "±")
	if !hasSpread {
		number, spreadText, hasSpread = strings.Cut(value, "+-")
	}
	v, err := strconv.ParseFloat(number, 64)
	if err != nil || !finite(v) || v < spec.low || v > spec.high {
		return 0, 0, fmt.Errorf("value must be a number from %g through %g", spec.low, spec.high)
	}
	spread := spec.spread
	if hasSpread {
		if spec.spread < 0 {
			return 0, 0, errors.New("a ceiling does not take a tolerance")
		}
		spread, err = strconv.ParseFloat(spreadText, 64)
		if err != nil || !finite(spread) || spread < 0 || spread > math.Abs(spec.high-spec.low) {
			return 0, 0, errors.New("tolerance must be a nonnegative number")
		}
	}
	return v, spread, nil
}

// Evaluate checks every expectation against a measured report, in order. A
// check without the data it needs fails and says why.
func Evaluate(r *Report, expectations []Expectation) []Check {
	checks := make([]Check, 0, len(expectations))
	for _, e := range expectations {
		checks = append(checks, e.evaluate(r))
	}
	return checks
}

// Passed reports whether every check passed; no checks pass.
func Passed(checks []Check) bool {
	for _, c := range checks {
		if c.Status != "pass" {
			return false
		}
	}
	return true
}

func (e Expectation) evaluate(r *Report) Check {
	c := Check{Name: e.key}
	verdict := func(ok bool, actual, message string) Check {
		c.Status, c.Actual, c.Message = "fail", actual, message
		if ok {
			c.Status, c.Message = "pass", ""
		}
		return c
	}
	noVideo := func() Check { return verdict(false, "", "the file has no video stream") }
	noAudio := func() Check { return verdict(false, "", "the file has no audio stream") }
	switch e.key {
	case "fps":
		c.Expected = number(e.value) + "±" + number(e.spread)
		if r.Video == nil {
			return noVideo()
		}
		if r.Video.FPS == nil {
			return verdict(false, "", "fewer than two frames; no frame rate")
		}
		actual := number(*r.Video.FPS)
		if r.Video.Timing != "cfr" {
			return verdict(false, actual, "frame rate is not constant: "+timingSummary(r.Video))
		}
		return verdict(math.Abs(*r.Video.FPS-e.value) <= e.spread+1e-9, actual, "measured "+actual+" fps")
	case "size":
		c.Expected = fmt.Sprintf("%dx%d", e.width, e.height)
		if r.Video == nil {
			return noVideo()
		}
		actual := fmt.Sprintf("%dx%d", r.Video.Width, r.Video.Height)
		return verdict(r.Video.Width == e.width && r.Video.Height == e.height, actual, "display size is "+actual)
	case "duration":
		c.Expected = number(e.value) + "±" + number(e.spread)
		actual := number(float64(r.Container.DurationMS) / 1000)
		return verdict(math.Abs(float64(r.Container.DurationMS)/1000-e.value) <= e.spread+1e-9, actual, "container duration is "+actual+" s")
	case "true-peak":
		c.Expected = "at most " + number(e.value)
		if r.Audio == nil {
			return noAudio()
		}
		if r.Audio.TruePeakDBTP == nil {
			return verdict(r.Audio.ClippedSamples == 0 && r.Audio.PeakDBFS == nil, "", "true peak was not measured (silent audio or more than eight channels)")
		}
		actual := number(*r.Audio.TruePeakDBTP)
		return verdict(*r.Audio.TruePeakDBTP <= e.value, actual, "true peak is "+actual+" dBTP")
	case "lufs":
		c.Expected = number(e.value) + "±" + number(e.spread)
		if r.Audio == nil {
			return noAudio()
		}
		if r.Audio.IntegratedLUFS == nil {
			return verdict(false, "", "integrated loudness was not measured (silent audio or more than eight channels)")
		}
		actual := number(*r.Audio.IntegratedLUFS)
		return verdict(math.Abs(*r.Audio.IntegratedLUFS-e.value) <= e.spread+1e-9, actual, "integrated loudness is "+actual+" LUFS")
	case "cfr":
		c.Expected = "cfr"
		if r.Video == nil {
			return noVideo()
		}
		return verdict(r.Video.Timing == "cfr", r.Video.Timing, timingSummary(r.Video))
	case "no-drops":
		c.Expected = "0"
		if r.Video == nil {
			return noVideo()
		}
		message := fmt.Sprintf("%d gaps, %d missing frames", r.Video.GapCount, r.Video.MissingFrames)
		if len(r.Video.Gaps) > 0 {
			message += fmt.Sprintf("; first at %d ms", r.Video.Gaps[0].AtMS)
		}
		return verdict(r.Video.GapCount == 0, strconv.Itoa(r.Video.MissingFrames), message)
	case "no-black":
		c.Expected = "0"
		if r.Video == nil {
			return noVideo()
		}
		return verdict(len(r.Video.Black) == 0, strconv.Itoa(len(r.Video.Black)), intervals("black", r.Video.Black))
	case "no-freeze":
		c.Expected = "0"
		if r.Video == nil {
			return noVideo()
		}
		return verdict(len(r.Video.Freeze) == 0, strconv.Itoa(len(r.Video.Freeze)), intervals("frozen", r.Video.Freeze))
	case "no-clipping":
		c.Expected = "0"
		if r.Audio == nil {
			return noAudio()
		}
		return verdict(r.Audio.ClippedSamples == 0, strconv.FormatInt(r.Audio.ClippedSamples, 10), fmt.Sprintf("%d samples exceed full scale", r.Audio.ClippedSamples))
	case "faststart":
		c.Expected = "true"
		if r.Container.FastStart == nil {
			return verdict(false, "", "fast start applies only to MP4/MOV files")
		}
		actual := strconv.FormatBool(*r.Container.FastStart)
		return verdict(*r.Container.FastStart, actual, "moov is stored after the media data")
	case "audio":
		c.Expected = "true"
		return verdict(r.Audio != nil, strconv.FormatBool(r.Audio != nil), "the file has no audio stream")
	case "no-audio":
		c.Expected = "false"
		return verdict(r.Audio == nil, strconv.FormatBool(r.Audio != nil), "the file has an audio stream")
	case "video":
		c.Expected = "true"
		return verdict(r.Video != nil, strconv.FormatBool(r.Video != nil), "the file has no video stream")
	}
	return verdict(false, "", "unknown expectation")
}

func timingSummary(v *Video) string {
	if v.Timing == "unknown" {
		return "fewer than two frames"
	}
	return fmt.Sprintf("frame intervals %s..%s ms, median %s ms; %d gaps, %d duplicate timestamps", number(v.IntervalMS.Min), number(v.IntervalMS.Max), number(v.IntervalMS.Median), v.GapCount, v.DuplicateCount)
}

func intervals(kind string, list []Interval) string {
	if len(list) == 0 {
		return "no " + kind + " intervals"
	}
	return fmt.Sprintf("%d %s intervals; first %d..%d ms", len(list), kind, list[0].StartMS, list[0].EndMS)
}

func number(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
