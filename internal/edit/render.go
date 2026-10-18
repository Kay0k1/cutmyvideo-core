package edit

import (
	"errors"
	"math"
	"regexp"
)

// AudioNormalize explicitly selects EBU R128 normalization of each completed
// output timeline. Pointers distinguish a missing target from a true peak of 0.
type AudioNormalize struct {
	IntegratedLUFS  *float64 `json:"integrated_lufs"`
	TruePeakDBTP    *float64 `json:"true_peak_dbtp"`
	LoudnessRangeLU *float64 `json:"loudness_range_lu"`
}

// Canvas contains the cropped display picture without stretching or removing
// pixels. Dimensions describe the final encoded frame, including its padding.
type Canvas struct {
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Fit        string `json:"fit"`
	Background string `json:"background"`
}

// AudioReplace selects a separate recording on the original source timeline.
// A positive offset places replacement timestamp zero after source timestamp
// zero. Missing audio before/after that recording becomes silence. HTTP uses
// SourceID; local callers use Input. Neither interface accepts the other's ref.
type AudioReplace struct {
	SourceID   string `json:"source_id,omitempty"`
	Input      string `json:"input,omitempty"`
	AudioTrack *int   `json:"audio_track,omitempty"`
	OffsetMS   int64  `json:"offset_ms"`
}

var canvasColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func HasRenderOptions(r ExportRequest) bool {
	return r.Canvas != nil || r.AudioNormalize != nil || r.AudioReplace != nil
}

// ValidateRenderOptions is shared by admission, preflight and every producer.
// It never changes caller-owned option values.
func ValidateRenderOptions(r ExportRequest) error {
	if !HasRenderOptions(r) {
		return nil
	}
	if r.CutMode != "accurate" || r.Format == "mkv" {
		return errors.New("canvas, audio_normalize and audio_replace require accurate MP4 or MP3; copy mode cannot apply rendering options")
	}
	if r.SubtitleMode == "preserve" {
		return errors.New("rendering options require subtitles omitted; subtitle preservation requires copy mode")
	}
	if v := r.Canvas; v != nil {
		if r.Format != "mp4" {
			return errors.New("canvas requires accurate MP4")
		}
		if r.Quality != "best" {
			return errors.New("canvas has explicit output dimensions and requires quality best")
		}
		if v.Width < 2 || v.Height < 2 || v.Width > 4096 || v.Height > 4096 || v.Width%2 != 0 || v.Height%2 != 0 {
			return errors.New("canvas width and height must be even integers between 2 and 4096")
		}
		if v.Fit != "contain" || !canvasColor.MatchString(v.Background) {
			return errors.New("canvas requires fit contain and a solid #RRGGBB background")
		}
		if r.Rendition == "draft" {
			return errors.New("explicit canvas dimensions require final rendition")
		}
	}
	if r.AudioNormalize != nil || r.AudioReplace != nil {
		if r.NoAudio {
			return errors.New("audio_normalize and audio_replace conflict with no_audio")
		}
	}
	if v := r.AudioNormalize; v != nil {
		if !finiteTarget(v.IntegratedLUFS, -70, -5) || !finiteTarget(v.TruePeakDBTP, -9, 0) || !finiteTarget(v.LoudnessRangeLU, 1, 50) {
			return errors.New("audio_normalize requires finite integrated_lufs (-70..-5), true_peak_dbtp (-9..0) and loudness_range_lu (1..50) targets")
		}
	}
	if v := r.AudioReplace; v != nil {
		if (v.SourceID == "") == (v.Input == "") {
			return errors.New("audio_replace requires exactly one source_id (HTTP) or input (local)")
		}
		if r.AudioTrack != nil {
			return errors.New("audio_replace conflicts with original audio_track; select audio_replace.audio_track instead")
		}
		if v.AudioTrack != nil && *v.AudioTrack < 0 {
			return errors.New("audio_replace.audio_track must be a nonnegative inspected stream index")
		}
		if v.OffsetMS < -24*3600000 || v.OffsetMS > 24*3600000 {
			return errors.New("audio_replace.offset_ms must be between -86400000 and 86400000")
		}
	}
	return nil
}

func finiteTarget(value *float64, low, high float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= low && *value <= high
}

func CloneAudioNormalize(v *AudioNormalize) *AudioNormalize {
	if v == nil {
		return nil
	}
	r := *v
	if v.IntegratedLUFS != nil {
		x := *v.IntegratedLUFS
		r.IntegratedLUFS = &x
	}
	if v.TruePeakDBTP != nil {
		x := *v.TruePeakDBTP
		r.TruePeakDBTP = &x
	}
	if v.LoudnessRangeLU != nil {
		x := *v.LoudnessRangeLU
		r.LoudnessRangeLU = &x
	}
	return &r
}
func CloneCanvas(v *Canvas) *Canvas {
	if v == nil {
		return nil
	}
	r := *v
	return &r
}
func CloneAudioReplace(v *AudioReplace) *AudioReplace {
	if v == nil {
		return nil
	}
	r := *v
	if v.AudioTrack != nil {
		x := *v.AudioTrack
		r.AudioTrack = &x
	}
	return &r
}
