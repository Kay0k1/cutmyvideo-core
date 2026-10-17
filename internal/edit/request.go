package edit

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateExportRequest preserves normalization order, including mutations made
// before a later validation error. Collection normalization owns its copies.
func ValidateExportRequest(r *ExportRequest, c Limits, s Source) error {
	if err := ValidateCropMode(*r); err != nil {
		return err
	}
	if len(r.Ranges) == 0 {
		return errors.New("choose at least one range; mark a start and an end before exporting")
	}
	if len(r.Ranges) > c.MaxRanges {
		return fmt.Errorf("choose between 1 and %d ranges (received %d); remove extra ranges or submit another job", c.MaxRanges, len(r.Ranges))
	}
	if r.Format != "mp4" && r.Format != "mp3" && r.Format != "mkv" {
		return errors.New("format must be mp4, mp3, or mkv")
	}
	if r.Quality != "1080p" && r.Quality != "720p" && r.Quality != "best" {
		return errors.New("quality must be 1080p, 720p, or best")
	}
	if r.CutMode != "accurate" && r.CutMode != "copy" {
		return errors.New("cut_mode must be accurate or copy")
	}
	if r.Rendition != "" && r.Rendition != "final" && r.Rendition != "draft" {
		return errors.New("rendition must be final or draft")
	}
	if r.Rendition == "draft" {
		if r.Format != "mp4" || r.CutMode != "accurate" {
			return errors.New("draft rendition requires accurate MP4; choose final to preserve original streams")
		}
		r.Quality = "720p"
	} else {
		r.Rendition = ""
	}
	if r.Format == "mp3" && r.CutMode == "copy" {
		return errors.New("MP3 export requires accurate mode; preserving arbitrary input audio as MP3 is not possible")
	}
	if r.Format == "mkv" && r.CutMode != "copy" {
		return errors.New("MKV export requires copy mode; choose MP4 for accurate cutting")
	}
	if r.OutputMode != "" && r.OutputMode != "separate" && r.OutputMode != "joined" {
		return errors.New("output_mode must be separate or joined")
	}
	if r.SubtitleMode != "" && r.SubtitleMode != "omit" && r.SubtitleMode != "preserve" {
		return errors.New("subtitle_mode must be omit or preserve")
	}
	if r.AudioTrack != nil && *r.AudioTrack < 0 {
		return errors.New("audio_track must be a nonnegative source stream index; inspect the source tracks first")
	}
	if r.NoAudio && r.AudioTrack != nil {
		return errors.New("choose audio_track or no_audio, not both")
	}
	if r.NoAudio && r.Format == "mp3" {
		return errors.New("MP3 export requires audio; enable audio or choose a video format")
	}
	if r.AudioTrack != nil && !s.HasLocalMedia {
		return errors.New("audio track selection requires an uploaded file; upload the source to select its audio track")
	}
	if r.SubtitleMode == "preserve" && (r.CutMode != "copy" || r.Format == "mp3") {
		return errors.New("subtitle preservation requires MP4 or MKV copy mode; choose copy mode or omit subtitles")
	}
	if r.SubtitleMode == "preserve" && !s.HasLocalMedia {
		return errors.New("subtitle preservation requires an uploaded file; upload the source or omit subtitles")
	}
	// Persist defaults exactly as older requests did. Idempotency continues to
	// return the original owner/key pair regardless of later request changes.
	if r.OutputMode == "separate" {
		r.OutputMode = ""
	}
	if r.SubtitleMode == "omit" {
		r.SubtitleMode = ""
	}
	var total int64
	for i := range r.Ranges {
		v := &r.Ranges[i]
		v.Label = strings.TrimSpace(v.Label)
		context := fmt.Sprintf("range %d", i+1)
		if v.Label != "" {
			context += fmt.Sprintf(" (%q)", v.Label)
		}
		if len(v.Label) > 200 {
			return fmt.Errorf("%s: shorten the label to at most 200 UTF-8 bytes", context)
		}
		if v.StartMS < 0 {
			return fmt.Errorf("%s: start_ms must be at least 0", context)
		}
		if v.EndMS <= v.StartMS {
			return fmt.Errorf("%s: set end_ms after start_ms (%d)", context, v.StartMS)
		}
		if v.EndMS > s.DurationMS {
			return fmt.Errorf("%s: set end_ms at or before the source duration (%d ms)", context, s.DurationMS)
		}
		if v.EndMS-v.StartMS > c.MaxRangeMS {
			return fmt.Errorf("%s: shorten the range to at most %d ms", context, c.MaxRangeMS)
		}
		duration := v.EndMS - v.StartMS
		if duration > c.MaxJobMS-total {
			return fmt.Errorf("%s: total selected duration exceeds %d ms; shorten or remove ranges", context, c.MaxJobMS)
		}
		total += duration
	}
	return nil
}
