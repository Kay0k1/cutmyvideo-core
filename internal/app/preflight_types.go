package app

import (
	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediapreflight"
)

type PreflightCheck = mediapreflight.Check
type PreflightWarning = mediapreflight.Warning
type PreflightSource = mediapreflight.Source
type PreflightVideo = mediapreflight.Video

// PreflightResult reports the normalized request and known prerequisites.
// Valid means those checks passed, not that export, decoding, output size,
// storage admission or publication is guaranteed. Execution rechecks its input.
type PreflightResult struct {
	Version       int                `json:"version"`
	Valid         bool               `json:"valid"`
	Source        PreflightSource    `json:"source"`
	Request       ExportRequest      `json:"request"`
	RequestedMS   int64              `json:"requested_duration_ms"`
	OutputCount   int                `json:"output_count"`
	TrackManifest []TrackSelection   `json:"track_manifest"`
	Video         *PreflightVideo    `json:"video,omitempty"`
	Checks        []PreflightCheck   `json:"checks"`
	Warnings      []PreflightWarning `json:"warnings"`
}

// preflightRequestResult requires an already validated request. Its slices and
// pointers belong to this operation; callers must not reuse them for execution.
func preflightRequestResult(source Source, request ExportRequest) PreflightResult {
	return mediaPreflightResult(mediapreflight.RequestResult(PreflightSource{DurationMS: source.DurationMS, Width: source.Width, Height: source.Height, DisplayWidth: source.DisplayWidth, DisplayHeight: source.DisplayHeight}, edit.ExportRequest(request)))
}

func addPreflightDeferred(result *PreflightResult, name, code, message string) {
	report := mediaPreflightReport(*result)
	mediapreflight.AddDeferred(&report, name, code, message)
	*result = mediaPreflightResult(report)
}
