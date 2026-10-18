// Package edit defines portable edit decisions and their shared validation.
// It has no dependency on media tools, HTTP, storage or service configuration.
package edit

type Range struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Label   string `json:"label"`
}

type ExportRequest struct {
	SourceID       string          `json:"source_id"`
	Ranges         []Range         `json:"ranges"`
	Format         string          `json:"format"`
	Quality        string          `json:"quality"`
	CutMode        string          `json:"cut_mode"`
	OutputMode     string          `json:"output_mode,omitempty"`
	AudioTrack     *int            `json:"audio_track,omitempty"`
	NoAudio        bool            `json:"no_audio,omitempty"`
	SubtitleMode   string          `json:"subtitle_mode,omitempty"`
	Crop           *Crop           `json:"crop,omitempty"`
	AudioNormalize *AudioNormalize `json:"audio_normalize,omitempty"`
	Canvas         *Canvas         `json:"canvas,omitempty"`
	AudioReplace   *AudioReplace   `json:"audio_replace,omitempty"`
	CleanupSource  bool            `json:"cleanup_source,omitempty"`
	// Rendition selects a draft without changing the edit plan.
	Rendition string `json:"rendition,omitempty"`
}

// Segment records actual source boundaries and the resulting joined
// timeline in user-specified order. Copy boundaries may include keyframe preroll.
type Segment struct {
	Index         int    `json:"index"`
	Label         string `json:"label"`
	SourceStartMS int64  `json:"source_start_ms"`
	SourceEndMS   int64  `json:"source_end_ms"`
	OutputStartMS int64  `json:"output_start_ms"`
	OutputEndMS   int64  `json:"output_end_ms"`
}

// Track identifies an absolute source stream index, rather than an audio ordinal.
type Track struct {
	Index           int    `json:"index"`
	Type            string `json:"type"`
	Codec           string `json:"codec"`
	Language        string `json:"language,omitempty"`
	Title           string `json:"title,omitempty"`
	Channels        int    `json:"channels,omitempty"`
	SampleRate      string `json:"sample_rate,omitempty"`
	Default         bool   `json:"default"`
	AttachedPicture bool   `json:"attached_picture,omitempty"`
}

// TrackSelection explains every included or excluded source stream.
type TrackSelection struct {
	Track
	Included bool   `json:"included"`
	Reason   string `json:"reason"`
}

// Limits apply to a selected edit or collection, independently of execution.
type Limits struct {
	MaxRanges            int
	MaxRangeMS, MaxJobMS int64
}

// Source contains only the source facts needed to validate an edit. It never
// carries a filesystem path, network address, credentials or persistence state.
type Source struct {
	ID            string
	DurationMS    int64
	HasLocalMedia bool
}

// ArgumentError identifies invalid edit decisions for interface-specific error
// adapters. Its message remains the original shared validation diagnostic.
type ArgumentError struct{ Message string }

func (e *ArgumentError) Error() string { return e.Message }
