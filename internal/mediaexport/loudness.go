package mediaexport

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

// LoudnessMeasurement is an internal first-pass EBU R128 measurement. Missing
// pointers describe a silent signal, never NaN or infinity in public JSON.
type LoudnessMeasurement struct {
	IntegratedLUFS, TruePeakDBTP, LoudnessRangeLU *float64
	Threshold, Offset                             *float64
}

func ParseLoudnessReport(data []byte) (LoudnessMeasurement, error) {
	text := strings.TrimSpace(string(data))
	start := strings.LastIndex(text, "\n{")
	if start >= 0 {
		start++
	} else if strings.HasPrefix(text, "{") {
		start = 0
	}
	if start < 0 {
		return LoudnessMeasurement{}, errors.New("media tool did not produce a complete loudness report")
	}
	end := strings.Index(text[start:], "\n}")
	if end < 0 {
		return LoudnessMeasurement{}, errors.New("media tool did not produce a complete loudness report")
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(text[start:start+end+2]), &values); err != nil {
		return LoudnessMeasurement{}, errors.New("media tool returned an invalid loudness report")
	}
	keys := []string{"input_i", "input_tp", "input_lra", "input_thresh", "output_i", "output_tp", "output_lra", "output_thresh", "normalization_type", "target_offset"}
	if len(values) != len(keys) {
		return LoudnessMeasurement{}, errors.New("media tool returned an incomplete loudness report")
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return LoudnessMeasurement{}, errors.New("media tool returned an incomplete loudness report")
		}
	}
	parse := func(key string) (*float64, error) {
		if values[key] == "-inf" || values[key] == "inf" {
			return nil, nil
		}
		v, err := strconv.ParseFloat(values[key], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1000 {
			return nil, errors.New("media tool returned nonfinite loudness metrics")
		}
		return &v, nil
	}
	var r LoudnessMeasurement
	var err error
	if r.IntegratedLUFS, err = parse("input_i"); err != nil {
		return r, err
	}
	if r.TruePeakDBTP, err = parse("input_tp"); err != nil {
		return r, err
	}
	if r.LoudnessRangeLU, err = parse("input_lra"); err != nil {
		return r, err
	}
	if r.Threshold, err = parse("input_thresh"); err != nil {
		return r, err
	}
	if r.Offset, err = parse("target_offset"); err != nil {
		return r, err
	}
	if r.IntegratedLUFS == nil {
		if r.TruePeakDBTP == nil {
			r.LoudnessRangeLU, r.Threshold, r.Offset = nil, nil, nil
		}
	} else if r.TruePeakDBTP == nil || r.LoudnessRangeLU == nil || r.Threshold == nil || r.Offset == nil {
		return r, errors.New("media tool returned inconsistent loudness metrics")
	}
	return r, nil
}

func LoudnessFilter(target edit.AudioNormalize, measurement *LoudnessMeasurement) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	base := "loudnorm=I=" + f(*target.IntegratedLUFS) + ":TP=" + f(*target.TruePeakDBTP) + ":LRA=" + f(*target.LoudnessRangeLU)
	if measurement == nil {
		return base + ":print_format=json"
	}
	if measurement.IntegratedLUFS == nil {
		return "anull"
	}
	// Explicitly use dynamic normalization in both passes. The measured gain
	// correction is calibrated on this exact joined/selected timeline. There is
	// no hidden change from a requested linear mode to dynamic processing.
	return base + ":measured_I=" + f(*measurement.IntegratedLUFS) + ":measured_TP=" + f(*measurement.TruePeakDBTP) + ":measured_LRA=" + f(*measurement.LoudnessRangeLU) + ":measured_thresh=" + f(*measurement.Threshold) + ":offset=" + f(*measurement.Offset) + ":linear=false"
}
