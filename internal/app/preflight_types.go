package app

// PreflightCheck distinguishes verified prerequisites from work which can only
// be checked while exporting. A preflight report does not authorize execution.
type PreflightCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // passed or deferred
}

type PreflightWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PreflightSource contains public inspection metadata, never a local server
// path, an extracted media address or credentials.
type PreflightSource struct {
	DurationMS    int64 `json:"duration_ms"`
	Width         int   `json:"width,omitempty"`
	Height        int   `json:"height,omitempty"`
	DisplayWidth  int   `json:"display_width,omitempty"`
	DisplayHeight int   `json:"display_height,omitempty"`
}

type PreflightVideo struct {
	Width    int  `json:"width"`
	Height   int  `json:"height"`
	HDRToSDR bool `json:"hdr_to_sdr"`
}

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
	result := PreflightResult{Version: 1, Valid: true, Source: PreflightSource{
		DurationMS: source.DurationMS, Width: source.Width, Height: source.Height,
		DisplayWidth: source.DisplayWidth, DisplayHeight: source.DisplayHeight,
	}, Request: request, OutputCount: len(request.Ranges),
		TrackManifest: []TrackSelection{}, Checks: []PreflightCheck{{Name: "request", Status: "passed"}},
		Warnings: []PreflightWarning{},
	}
	if request.OutputMode == "joined" {
		result.OutputCount = 1
	}
	for _, r := range request.Ranges {
		result.RequestedMS += r.EndMS - r.StartMS
	}
	return result
}

func addPreflightDeferred(result *PreflightResult, name, code, message string) {
	result.Checks = append(result.Checks, PreflightCheck{Name: name, Status: "deferred"})
	result.Warnings = append(result.Warnings, PreflightWarning{Code: code, Message: message})
}
