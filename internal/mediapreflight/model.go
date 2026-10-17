// Package mediapreflight plans advisory media checks without performing I/O.
package mediapreflight

import "github.com/Kay0k1/cutmyvideo-core/internal/edit"

// Check distinguishes verified prerequisites from work which can only
// be checked while exporting. A preflight report does not authorize execution.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // passed or deferred
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Source contains public inspection metadata, never a local server
// path, an extracted media address or credentials.
type Source struct {
	DurationMS    int64 `json:"duration_ms"`
	Width         int   `json:"width,omitempty"`
	Height        int   `json:"height,omitempty"`
	DisplayWidth  int   `json:"display_width,omitempty"`
	DisplayHeight int   `json:"display_height,omitempty"`
}

type Video struct {
	Width    int  `json:"width"`
	Height   int  `json:"height"`
	HDRToSDR bool `json:"hdr_to_sdr"`
}

// Result reports the normalized request and known prerequisites.
// Valid means those checks passed, not that export, decoding, output size,
// storage admission or publication is guaranteed. Execution rechecks its input.
type Result struct {
	Version       int                   `json:"version"`
	Valid         bool                  `json:"valid"`
	Source        Source                `json:"source"`
	Request       edit.ExportRequest    `json:"request"`
	RequestedMS   int64                 `json:"requested_duration_ms"`
	OutputCount   int                   `json:"output_count"`
	TrackManifest []edit.TrackSelection `json:"track_manifest"`
	Video         *Video                `json:"video,omitempty"`
	Checks        []Check               `json:"checks"`
	Warnings      []Warning             `json:"warnings"`
}

// RequestResult requires an already validated request. Its slices and
// pointers belong to this operation; callers must not reuse them for execution.
func RequestResult(source Source, request edit.ExportRequest) Result {
	result := Result{Version: 1, Valid: true, Source: Source{
		DurationMS: source.DurationMS, Width: source.Width, Height: source.Height,
		DisplayWidth: source.DisplayWidth, DisplayHeight: source.DisplayHeight,
	}, Request: request, OutputCount: len(request.Ranges),
		TrackManifest: []edit.TrackSelection{}, Checks: []Check{{Name: "request", Status: "passed"}},
		Warnings: []Warning{},
	}
	if request.OutputMode == "joined" {
		result.OutputCount = 1
	}
	for _, r := range request.Ranges {
		result.RequestedMS += r.EndMS - r.StartMS
	}
	return result
}

func AddDeferred(result *Result, name, code, message string) {
	result.Checks = append(result.Checks, Check{Name: name, Status: "deferred"})
	result.Warnings = append(result.Warnings, Warning{Code: code, Message: message})
}
