package edit

import (
	"errors"
	"math"
)

// Speed is an exact source-to-output playback rate, not a floating point hint.
type Speed struct {
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`
}

func IsAudioOnly(format string) bool {
	return format == "mp3" || format == "wav" || format == "flac" || format == "m4a"
}

func IsLosslessAudio(format string) bool { return format == "wav" || format == "flac" }
func ValidOutputFormat(format string) bool {
	return format == "mp4" || format == "mkv" || IsAudioOnly(format)
}

func NormalizeSpeed(value *Speed) (*Speed, error) {
	if value == nil {
		return nil, nil
	}
	n, d := value.Numerator, value.Denominator
	if n < 1 || n > 1000 || d < 1 || d > 1000 || 2*n < d || n > 2*d {
		return nil, errors.New("speed requires integer numerator and denominator between 1 and 1000 and a rate between 0.5 and 2")
	}
	a, b := n, d
	for b != 0 {
		a, b = b, a%b
	}
	return &Speed{Numerator: n / a, Denominator: d / a}, nil
}

func CloneSpeed(value *Speed) *Speed {
	if value == nil {
		return nil
	}
	r := *value
	return &r
}

// OutputDurationMS rounds one cumulative source position to the nearest output
// millisecond. Map cumulative positions, rather than summing rounded fragments.
func OutputDurationMS(sourceMS int64, speed *Speed) (int64, error) {
	if sourceMS < 0 {
		return 0, errors.New("source duration must be nonnegative")
	}
	r, err := NormalizeSpeed(speed)
	if err != nil {
		return 0, err
	}
	if r == nil {
		return sourceMS, nil
	}
	n, d := int64(r.Numerator), int64(r.Denominator)
	if sourceMS > (math.MaxInt64-n/2)/d {
		return 0, errors.New("speed output timeline exceeds the supported duration")
	}
	return (sourceMS*d + n/2) / n, nil
}

// RequiresRenderPipeline keeps optional transformations and lossless audio on
// the same bounded, verified timeline producer.
func RequiresRenderPipeline(r ExportRequest) bool {
	return HasRenderOptions(r) || IsLosslessAudio(r.Format)
}
