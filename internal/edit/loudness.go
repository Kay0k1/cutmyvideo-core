package edit

// LoudnessOptions selects one bounded source-timeline inspection window.
// EndMS zero means the inspected finite source duration.
type LoudnessOptions struct {
	StartMS    int64 `json:"start_ms,omitempty"`
	EndMS      int64 `json:"end_ms,omitempty"`
	AudioTrack *int  `json:"audio_track,omitempty"`
}

// LoudnessResult contains gated EBU R128 metrics. A silent signal has null
// metrics; AnalyzedEndMS records actual decoded coverage, not assumed silence.
type LoudnessResult struct {
	Version         int      `json:"version"`
	StartMS         int64    `json:"start_ms"`
	EndMS           int64    `json:"end_ms"`
	AnalyzedEndMS   int64    `json:"analyzed_end_ms"`
	AudioTrack      int      `json:"audio_track"`
	IntegratedLUFS  *float64 `json:"integrated_lufs"`
	TruePeakDBTP    *float64 `json:"true_peak_dbtp"`
	LoudnessRangeLU *float64 `json:"loudness_range_lu"`
}
