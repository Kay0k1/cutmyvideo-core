package edit

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// CollectionPlan is a portable collection of independent edits of one source.
// Input is a local path for the engine; HTTP requests use an existing source_id.
type CollectionPlan struct {
	Version int               `json:"version"`
	Input   string            `json:"input,omitempty"`
	Options CollectionOptions `json:"options,omitempty"`
	Clips   []CollectionClip  `json:"clips"`
}

type CollectionClip struct {
	ID      string            `json:"id"`
	Label   string            `json:"label,omitempty"`
	Ranges  []Range           `json:"ranges"`
	Options CollectionOptions `json:"options,omitempty"`
}

// Empty strings and nil pointers inherit collection defaults. NoAudio uses a
// pointer so false can enable inherited audio. AudioTrack -1 selects automatic
// audio. ClearCrop explicitly removes an inherited crop. Crop and ClearCrop
// together are invalid; resetting audio does not implicitly change NoAudio.
type CollectionOptions struct {
	Format       string `json:"format,omitempty"`
	Quality      string `json:"quality,omitempty"`
	CutMode      string `json:"cut_mode,omitempty"`
	AudioTrack   *int   `json:"audio_track,omitempty"`
	NoAudio      *bool  `json:"no_audio,omitempty"`
	SubtitleMode string `json:"subtitle_mode,omitempty"`
	Crop         *Crop  `json:"crop,omitempty"`
	ClearCrop    bool   `json:"clear_crop,omitempty"`
}

// ClipIDs is a selection, not an ordering instruction. Results retain plan
// order. An empty selection exports all clips. Rendition defaults to final.
type CollectionExecution struct {
	ClipIDs   []string `json:"clip_ids,omitempty"`
	Rendition string   `json:"rendition,omitempty"`
}

type PreparedCollectionClip struct {
	ID          string        `json:"id"`
	Label       string        `json:"label,omitempty"`
	Request     ExportRequest `json:"request"`
	RequestedMS int64         `json:"requested_duration_ms"`
}

// CollectionFieldError locates a failure in a saved collection. Field is a
// JSON-style relative field path for ClipID, or a top-level path if ID is empty.
// Unwrap preserves typed media, cancellation and filesystem failures.
type CollectionFieldError struct {
	ClipID string `json:"clip_id,omitempty"`
	Field  string `json:"field"`
	Err    error  `json:"-"`
}

func (e *CollectionFieldError) Error() string {
	if e.ClipID != "" {
		return fmt.Sprintf("clip %q %s: %v", e.ClipID, e.Field, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Field, e.Err)
}
func (e *CollectionFieldError) Unwrap() error { return e.Err }

func collectionInvalid(id, field, message string) error {
	return &CollectionFieldError{ClipID: id, Field: field, Err: &ArgumentError{Message: message}}
}

var collectionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var collectionReservedIDPattern = regexp.MustCompile(`^(con|prn|aux|nul|com[1-9]|lpt[1-9])$`)

// ValidCollectionID applies the same portable ID rules to collection consumers.
func ValidCollectionID(id string) bool {
	return collectionIDPattern.MatchString(id) && !collectionReservedIDPattern.MatchString(id)
}

func NormalizeCollectionRendition(value string) (string, error) {
	if value == "" {
		return "final", nil
	}
	if value != "final" && value != "draft" {
		return "", collectionInvalid("", "rendition", "must be final or draft")
	}
	return value, nil
}

// NormalizeCollection validates all IDs, labels and structural bounds first,
// then validates only selected clips' media settings and intervals. Unselected
// edits may remain unfinished. Limits apply to the selected collection as a
// whole, rather than resetting for each clip. Caller data is never mutated.
func NormalizeCollection(plan CollectionPlan, execution CollectionExecution, c Limits, source Source) ([]PreparedCollectionClip, error) {
	render, err := NormalizeCollectionRendition(execution.Rendition)
	if err != nil {
		return nil, err
	}
	if plan.Version != 1 {
		return nil, collectionInvalid("", "version", "must be 1")
	}
	if len(plan.Clips) == 0 || len(plan.Clips) > 128 {
		return nil, collectionInvalid("", "clips", "must contain 1 through 128 clips")
	}
	ids := make(map[string]bool, len(plan.Clips))
	for i, clip := range plan.Clips {
		if !collectionIDPattern.MatchString(clip.ID) {
			return nil, collectionInvalid(clip.ID, fmt.Sprintf("clips[%d].id", i), "must match [a-z0-9][a-z0-9_-]{0,63}")
		}
		// These device names remain reserved on Windows even with rendition and
		// format suffixes. Reject them everywhere so saved plans are portable.
		if collectionReservedIDPattern.MatchString(clip.ID) {
			return nil, collectionInvalid(clip.ID, fmt.Sprintf("clips[%d].id", i), "must not be a reserved device name (con, prn, aux, nul, com1..9 or lpt1..9)")
		}
		if ids[clip.ID] {
			return nil, collectionInvalid(clip.ID, "id", "must be unique in the collection")
		}
		ids[clip.ID] = true
		if len(clip.Label) > 200 {
			return nil, collectionInvalid(clip.ID, "label", "must be at most 200 UTF-8 bytes")
		}
		if len(clip.Ranges) == 0 || len(clip.Ranges) > 128 {
			return nil, collectionInvalid(clip.ID, "ranges", "must contain 1 through 128 intervals")
		}
	}
	selected := make(map[string]bool, len(execution.ClipIDs))
	for i, id := range execution.ClipIDs {
		if !ids[id] {
			return nil, collectionInvalid(id, fmt.Sprintf("clip_ids[%d]", i), "does not identify a clip in the plan")
		}
		if selected[id] {
			return nil, collectionInvalid(id, "clip_ids", "selection must not contain duplicate IDs")
		}
		selected[id] = true
	}
	var result []PreparedCollectionClip
	var count int
	var total int64
	for _, clip := range plan.Clips {
		if len(selected) > 0 && !selected[clip.ID] {
			continue
		}
		request, err := collectionRequest(plan.Options, clip.Options)
		if err != nil {
			field := "options"
			var location *CollectionFieldError
			if errors.As(err, &location) {
				field = location.Field
			}
			return nil, &CollectionFieldError{ClipID: clip.ID, Field: field, Err: err}
		}
		request.SourceID = source.ID
		request.Ranges = append([]Range(nil), clip.Ranges...)
		request.OutputMode = "joined"
		if settingsErr := collectionValidateSettings(request); settingsErr != nil {
			return nil, &CollectionFieldError{ClipID: clip.ID, Field: settingsErr.Field, Err: settingsErr.Err}
		}
		if render == "draft" {
			if request.CutMode != "accurate" {
				return nil, collectionInvalid(clip.ID, "options.cut_mode", "draft requires accurate mode; copy boundaries cannot be previewed faithfully by re-encoding")
			}
			if request.Format != "mp4" {
				return nil, collectionInvalid(clip.ID, "options.format", "draft requires mp4; select final for mp3 or mkv")
			}
			request.Quality = "720p"
			request.Rendition = "draft"
		}
		var duration int64
		for i, r := range request.Ranges {
			prefix := fmt.Sprintf("ranges[%d]", i)
			if len(strings.TrimSpace(r.Label)) > 200 {
				return nil, collectionInvalid(clip.ID, prefix+".label", "must be at most 200 UTF-8 bytes")
			}
			if r.StartMS < 0 {
				return nil, collectionInvalid(clip.ID, prefix+".start_ms", "must be nonnegative")
			}
			if r.EndMS <= r.StartMS {
				return nil, collectionInvalid(clip.ID, prefix+".end_ms", "must be after start_ms")
			}
			if r.EndMS > source.DurationMS {
				return nil, collectionInvalid(clip.ID, prefix+".end_ms", fmt.Sprintf("must be at or before source duration (%d ms)", source.DurationMS))
			}
			if r.EndMS-r.StartMS > c.MaxRangeMS {
				return nil, collectionInvalid(clip.ID, prefix+".end_ms", fmt.Sprintf("interval exceeds %d ms", c.MaxRangeMS))
			}
			if r.EndMS-r.StartMS > c.MaxJobMS-total {
				return nil, collectionInvalid(clip.ID, prefix+".end_ms", fmt.Sprintf("selected collection duration exceeds %d ms", c.MaxJobMS))
			}
			duration += r.EndMS - r.StartMS
			total += r.EndMS - r.StartMS
		}
		count += len(request.Ranges)
		if count > c.MaxRanges {
			return nil, collectionInvalid(clip.ID, "ranges", fmt.Sprintf("selected collection exceeds %d intervals", c.MaxRanges))
		}
		if err = ValidateExportRequest(&request, c, source); err != nil {
			return nil, collectionInvalid(clip.ID, "options", err.Error())
		}
		result = append(result, PreparedCollectionClip{ID: clip.ID, Label: strings.TrimSpace(clip.Label), Request: request, RequestedMS: duration})
	}
	return result, nil
}

func collectionRequest(base, override CollectionOptions) (ExportRequest, error) {
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate"}
	for _, options := range []CollectionOptions{base, override} {
		if options.Crop != nil && options.ClearCrop {
			return request, collectionInvalid("", "options.crop", "crop and clear_crop cannot be used together")
		}
		if options.Format != "" {
			request.Format = options.Format
		}
		if options.Quality != "" {
			request.Quality = options.Quality
		}
		if options.CutMode != "" {
			request.CutMode = options.CutMode
		}
		if options.SubtitleMode != "" {
			request.SubtitleMode = options.SubtitleMode
		}
		if options.NoAudio != nil {
			request.NoAudio = *options.NoAudio
		}
		if options.AudioTrack != nil {
			index := *options.AudioTrack
			if index < -1 {
				return request, collectionInvalid("", "options.audio_track", "audio_track must be -1 (automatic) or a nonnegative stream index")
			}
			request.AudioTrack = nil
			if index >= 0 {
				request.AudioTrack = &index
			}
		}
		if options.ClearCrop {
			request.Crop = nil
		}
		if options.Crop != nil {
			crop := *options.Crop
			request.Crop = &crop
		}
	}
	return request, nil
}

func collectionValidateSettings(request ExportRequest) *CollectionFieldError {
	invalid := func(field, message string) *CollectionFieldError {
		return &CollectionFieldError{Field: "options." + field, Err: &ArgumentError{Message: message}}
	}
	if request.Format != "mp4" && request.Format != "mp3" && request.Format != "mkv" {
		return invalid("format", "must be mp4, mp3 or mkv")
	}
	if request.Quality != "best" && request.Quality != "720p" && request.Quality != "1080p" {
		return invalid("quality", "must be best, 720p or 1080p")
	}
	if request.CutMode != "accurate" && request.CutMode != "copy" {
		return invalid("cut_mode", "must be accurate or copy")
	}
	if request.SubtitleMode != "" && request.SubtitleMode != "omit" && request.SubtitleMode != "preserve" {
		return invalid("subtitle_mode", "must be omit or preserve")
	}
	if request.NoAudio && request.AudioTrack != nil {
		return invalid("audio_track", "audio_track conflicts with no_audio")
	}
	if request.NoAudio && request.Format == "mp3" {
		return invalid("no_audio", "mp3 requires audio")
	}
	if request.Format == "mp3" && request.CutMode == "copy" {
		return invalid("cut_mode", "mp3 requires accurate mode")
	}
	if request.Format == "mkv" && request.CutMode != "copy" {
		return invalid("cut_mode", "mkv requires copy mode")
	}
	if request.SubtitleMode == "preserve" && (request.CutMode != "copy" || request.Format == "mp3") {
		return invalid("subtitle_mode", "preserve requires video copy mode")
	}
	if err := ValidateCropMode(request); err != nil {
		return invalid("crop", err.Error())
	}
	return nil
}

// CollectionOutputFilename uses a validated clip ID; labels never become paths.
func CollectionOutputFilename(clip PreparedCollectionClip, rendition string) string {
	return clip.ID + "." + rendition + "." + clip.Request.Format
}

var _ error = (*CollectionFieldError)(nil)
