package app

import (
	"errors"
	"reflect"
	"testing"
)

// Preserve the existing method's concrete service arguments, even though the
// shared validator accepts only the neutral projections of these types.
var _ func(*ExportRequest, Config, Source) error = (*ExportRequest).Validate

func TestEditAdapterPreservesRequestNormalizationOrder(t *testing.T) {
	c := Config{MaxRanges: 4, MaxRangeMS: 1000, MaxJobMS: 2000}
	source := Source{ID: "source", Path: "local.media", DurationMS: 2000}
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "separate", SubtitleMode: "omit", Rendition: "final", Ranges: []Range{{StartMS: 100, EndMS: 900, Label: "  trimmed  "}}}
	if err := request.Validate(c, source); err != nil {
		t.Fatal(err)
	}
	if request.OutputMode != "" || request.SubtitleMode != "" || request.Rendition != "" || request.Ranges[0].Label != "trimmed" {
		t.Fatal("legacy request defaults or label normalization changed", request)
	}
	// Validation mutates earlier fields before a later range error. A value-copy
	// adapter which assigns only on success would silently change this behavior.
	request = ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "separate", SubtitleMode: "omit", Rendition: "draft", Ranges: []Range{{StartMS: 100, EndMS: 100, Label: "  trimmed  "}}}
	err := request.Validate(c, source)
	if err == nil || err.Error() != `range 1 ("trimmed"): set end_ms after start_ms (100)` {
		t.Fatal("legacy range diagnostic changed", err)
	}
	if request.Quality != "720p" || request.Rendition != "draft" || request.OutputMode != "" || request.SubtitleMode != "" || request.Ranges[0].Label != "trimmed" {
		t.Fatal("adapter discarded partial normalization on failure", request)
	}
	request.Crop = &Crop{Width: 2, Height: 1}
	request.Quality = "best"
	if err = request.Validate(c, source); err == nil || request.Quality != "best" {
		t.Fatal("crop validation no longer precedes rendition normalization", err)
	}
	if reflect.TypeOf(err) != reflect.TypeOf(errors.New("")) || LocalErrorCode(err) != "" {
		t.Fatal("legacy request validation acquired a typed media classification", err)
	}
}

func TestEditAdapterPreservesCollectionErrorChains(t *testing.T) {
	validate := func(options CollectionOptions) error {
		_, err := NormalizeCollection(CollectionPlan{Version: 1, Options: options,
			Clips: []CollectionClip{{ID: "one", Ranges: []Range{{EndMS: 500}}}}},
			CollectionExecution{}, Config{MaxRanges: 4, MaxRangeMS: 1000, MaxJobMS: 2000}, Source{Path: "local.media", DurationMS: 1000})
		return err
	}
	for _, test := range []struct {
		name, clip, field, message, diagnostic string
		depth                                  int
		err                                    func() error
	}{
		{"rendition", "", "rendition", "must be final or draft", "rendition: must be final or draft", 1,
			func() error { _, err := NormalizeCollectionRendition("unknown"); return err }},
		{"format", "one", "options.format", "must be mp4, mp3, mkv, wav, flac or m4a", `clip "one" options.format: must be mp4, mp3, mkv, wav, flac or m4a`, 1,
			func() error { return validate(CollectionOptions{Format: "gif"}) }},
		{"nested-crop", "one", "options.crop", "crop and clear_crop cannot be used together", `clip "one" options.crop: options.crop: crop and clear_crop cannot be used together`, 2,
			func() error { return validate(CollectionOptions{Crop: &Crop{Width: 1, Height: 1}, ClearCrop: true}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.err()
			var field *CollectionFieldError
			var problem *sourceProblem
			if !errors.As(err, &field) || field.ClipID != test.clip || field.Field != test.field {
				t.Fatal("collection error lost its public location", err)
			}
			if !errors.As(err, &problem) || problem.code != "invalid_argument" || problem.message != test.message || LocalErrorCode(err) != "invalid_argument" {
				t.Fatal("collection error lost its established diagnostic chain", err)
			}
			if err.Error() != test.diagnostic {
				t.Fatal("collection diagnostic text changed", err)
			}
			depth := 0
			for current := err; current != nil; current = errors.Unwrap(current) {
				if _, ok := current.(*CollectionFieldError); ok {
					depth++
				}
			}
			if depth != test.depth {
				t.Fatal("adapter changed nested field-error structure", depth, test.depth)
			}
		})
	}
}
