package subtitles

import "github.com/Kay0k1/cutmyvideo-core/internal/edit"

// RetimeRequest transforms supplied text against actual output maps.
// It never opens a media file, runs a tool, or infers a transcript.
type RetimeRequest struct {
	Version      int          `json:"version"`
	Format       string       `json:"format"`
	Text         string       `json:"text"`
	OutputFormat string       `json:"output_format,omitempty"`
	Outputs      []RetimeClip `json:"outputs"`
}

type RetimeClip struct {
	ClipID   string         `json:"clip_id"`
	Segments []edit.Segment `json:"segments"`
}

type Sidecar struct {
	ClipID   string `json:"clip_id"`
	Format   string `json:"format"`
	Text     string `json:"text"`
	CueCount int    `json:"cue_count"`
	SHA256   string `json:"sha256"`
}

type RetimeResult struct {
	Version int       `json:"version"`
	Outputs []Sidecar `json:"outputs"`
}

// RetimeError contains fixed public diagnostics, never source text.
type RetimeError struct {
	Code  string `json:"code"`
	Field string `json:"field"`
}

func (e *RetimeError) Error() string { return e.Code + ": " + e.Field }

// Text subtitles are extracted once in their source clock, clipped to the
// actual export bounds and placed on the new output clock. Subtitle packets
// cannot simply be copied: a cue can begin before a cut and span its boundary.
type Cue struct {
	StartMS, EndMS int64
	Text           string
}
