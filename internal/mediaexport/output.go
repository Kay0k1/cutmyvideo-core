package mediaexport

import (
	"fmt"
	"math"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func targetFailure(message string) error {
	return &mediainfo.Problem{Code: "target_size_unattainable", Message: message}
}

// ValidateTargetSize checks the operator ceiling before any media processing.
func ValidateTargetSize(c Config, request edit.ExportRequest) error {
	if request.TargetSizeBytes != 0 && (request.TargetSizeBytes < 0 || request.TargetSizeBytes >= math.MaxInt64 || request.TargetSizeBytes > c.MaxOutputBytes) {
		return &mediainfo.Problem{Code: "invalid_argument", Message: "target_size_bytes must be positive and at or below the configured output byte limit"}
	}
	return nil
}

func targetVideoRate(c Config, request edit.ExportRequest, duration int64, audio *mediainfo.Stream) (int64, error) {
	if err := ValidateTargetSize(c, request); err != nil {
		return 0, err
	}
	if request.TargetSizeBytes == 0 {
		return VideoBitrate(c, edit.Range{EndMS: duration}, request), nil
	}
	// Reserve fixed container overhead plus ten percent for muxing/rate-control
	// variation. A successful result is still checked against the exact target.
	usable := request.TargetSizeBytes - request.TargetSizeBytes/10 - 16384
	if usable <= 0 || duration <= 0 {
		return 0, targetFailure("target size cannot hold a complete MP4 with container overhead")
	}
	// Division first bounds arithmetic even at the largest operator ceiling.
	bits := float64(usable) * 8000 / float64(duration)
	if audio != nil {
		if audio.Channels == 1 {
			bits -= 128000
		} else {
			bits -= 192000
		}
	}
	if bits < 256000 {
		return 0, targetFailure("target size is too small for the complete duration, selected audio and 256 kbit/s video floor")
	}
	if bits > 32000000 {
		bits = 32000000
	}
	return int64(bits), nil
}

// LosslessAudioProfile fixes 24-bit output while preserving inspected cadence
// and channel count. No arbitrary resampling/downmix is silently substituted.
func LosslessAudioProfile(audio *mediainfo.Stream, format string) ([]string, error) {
	if audio == nil {
		return nil, ErrNoAudio
	}
	rate, err := strconv.Atoi(audio.SampleRate)
	if err != nil || rate < 8000 || rate > 192000 || audio.Channels < 1 || audio.Channels > 8 {
		return nil, &mediainfo.Problem{Code: "unsupported_stream", Message: "lossless audio requires an inspected sample rate between 8000 and 192000 Hz and 1 through 8 channels"}
	}
	args := []string{"-ar", strconv.Itoa(rate), "-ac", strconv.Itoa(audio.Channels)}
	if format == "wav" {
		return append(args, "-c:a", "pcm_s24le", "-rf64", "auto"), nil
	}
	return append(args, "-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24"), nil
}

func ValidateAudioOutput(info mediainfo.Info, source *mediainfo.Stream, format string) error {
	if !edit.IsAudioOnly(format) {
		return nil
	}
	a := info.SelectedAudio()
	if a == nil {
		return ErrNoAudio
	}
	if info.SelectedVideo() != nil {
		return fmt.Errorf("audio output unexpectedly contains video")
	}
	want := ""
	switch format {
	case "wav":
		want = "pcm_s24le"
	case "flac":
		want = "flac"
	case "m4a":
		want = "aac"
	case "mp3":
		want = "mp3"
	}
	if a.CodecName != want {
		return fmt.Errorf("audio output codec does not match the selected format")
	}
	if edit.IsLosslessAudio(format) && (source == nil || a.SampleRate != source.SampleRate || a.Channels != source.Channels || format == "flac" && a.BitsPerRawSample != 24) {
		return fmt.Errorf("lossless output does not preserve the inspected rate, channels and 24-bit profile")
	}
	if format == "m4a" && source != nil && (a.SampleRate != source.SampleRate || a.Channels != source.Channels || source.ExtradataHash != "" && a.ExtradataHash != "" && source.ExtradataHash != a.ExtradataHash) {
		return &mediainfo.Problem{Code: "copy_incompatible", Message: "M4A copy changed the selected AAC stream configuration"}
	}
	return nil
}
