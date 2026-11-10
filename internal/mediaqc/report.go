// Package mediaqc interprets measurements of a finished media file: frame
// timing, container layout and expectations. It does not run media tools.
package mediaqc

// MaxListed bounds the gap and duplicate lists of one report. Their counts are
// always complete; a list shorter than its count names only the first entries.
const MaxListed = 256

// SilenceThreshold is the sample magnitude (-60 dBFS) above which audio is
// considered audible when measuring leading and trailing silence.
const SilenceThreshold = 0.001

// Report is version 1 of a finished-file measurement. Times are milliseconds
// on the source timeline. Video, Audio and Sync are null when the file lacks
// the corresponding stream. Checks lists one entry per expectation.
type Report struct {
	Version   int       `json:"version"`
	Path      string    `json:"path"`
	StartMS   int64     `json:"start_ms"`
	EndMS     int64     `json:"end_ms"`
	Container Container `json:"container"`
	Video     *Video    `json:"video"`
	Audio     *Audio    `json:"audio"`
	Sync      *Sync     `json:"sync"`
	Checks    []Check   `json:"checks"`
	Passed    bool      `json:"passed"`
}

// Container describes the whole file. FastStart is null outside MP4/MOV.
type Container struct {
	Format     string   `json:"format"`
	DurationMS int64    `json:"duration_ms"`
	SizeBytes  int64    `json:"size_bytes"`
	BitRate    int64    `json:"bit_rate"`
	StartMS    int64    `json:"start_ms"`
	FastStart  *bool    `json:"faststart"`
	Streams    []Stream `json:"streams"`
}

// Stream reports the declared timing of every stream. Unknown values are null.
type Stream struct {
	Index      int    `json:"index"`
	Type       string `json:"type"`
	Codec      string `json:"codec"`
	StartMS    *int64 `json:"start_ms"`
	DurationMS *int64 `json:"duration_ms"`
}

type Color struct {
	Range     string `json:"range"`
	Primaries string `json:"primaries"`
	Transfer  string `json:"transfer"`
	Matrix    string `json:"matrix"`
}

type IntervalStats struct {
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
}

// Gap is a frame interval much longer than the nominal one: MissingFrames
// frames would fit, the first of them at AtMS.
type Gap struct {
	AtMS          int64 `json:"at_ms"`
	MissingFrames int   `json:"missing_frames"`
}

type Interval struct {
	StartMS int64 `json:"start_ms"`
	EndMS   int64 `json:"end_ms"`
}

// Video reports the selected video stream. Width and Height are display
// dimensions after sample aspect ratio and rotation. Frame timing comes from
// packet timestamps inside the window; Black, Freeze and AnalyzedEndMS come
// from decoding it. Timing is cfr, vfr or unknown (fewer than two frames).
type Video struct {
	Track             int           `json:"track"`
	Codec             string        `json:"codec"`
	Width             int           `json:"width"`
	Height            int           `json:"height"`
	CodedWidth        int           `json:"coded_width"`
	CodedHeight       int           `json:"coded_height"`
	Rotation          float64       `json:"rotation"`
	SampleAspectRatio string        `json:"sample_aspect_ratio"`
	PixelFormat       string        `json:"pix_fmt"`
	Color             Color         `json:"color"`
	RFrameRate        string        `json:"r_frame_rate"`
	AvgFrameRate      string        `json:"avg_frame_rate"`
	FrameCount        int           `json:"frame_count"`
	ExpectedFrames    int64         `json:"expected_frames"`
	FirstFrameMS      int64         `json:"first_frame_ms"`
	LastFrameMS       int64         `json:"last_frame_ms"`
	EndMS             int64         `json:"end_ms"`
	DurationMS        int64         `json:"duration_ms"`
	Timing            string        `json:"timing"`
	FPS               *float64      `json:"fps"`
	IntervalMS        IntervalStats `json:"interval_ms"`
	GapCount          int           `json:"gap_count"`
	MissingFrames     int           `json:"missing_frames"`
	Gaps              []Gap         `json:"gaps"`
	DuplicateCount    int           `json:"duplicate_count"`
	Duplicates        []int64       `json:"duplicates"`
	AnalyzedEndMS     int64         `json:"analyzed_end_ms"`
	Black             []Interval    `json:"black"`
	Freeze            []Interval    `json:"freeze"`
}

// Audio reports the selected audio stream decoded at its own sample rate.
// Loudness fields are null for silence or for more than eight channels.
// PeakDBFS is null when every sample is zero.
type Audio struct {
	Track             int      `json:"track"`
	Codec             string   `json:"codec"`
	SampleRate        int      `json:"sample_rate"`
	Channels          int      `json:"channels"`
	StartMS           int64    `json:"start_ms"`
	EndMS             int64    `json:"end_ms"`
	DurationMS        int64    `json:"duration_ms"`
	IntegratedLUFS    *float64 `json:"integrated_lufs"`
	TruePeakDBTP      *float64 `json:"true_peak_dbtp"`
	LoudnessRangeLU   *float64 `json:"loudness_range_lu"`
	PeakDBFS          *float64 `json:"peak_dbfs"`
	ClippedSamples    int64    `json:"clipped_samples"`
	LeadingSilenceMS  int64    `json:"leading_silence_ms"`
	TrailingSilenceMS int64    `json:"trailing_silence_ms"`
}

// Sync compares the decoded audio with the video frames: positive values mean
// audio starts, ends or lasts later or longer than the video.
type Sync struct {
	StartOffsetMS   int64 `json:"start_offset_ms"`
	EndOffsetMS     int64 `json:"end_offset_ms"`
	DurationDeltaMS int64 `json:"duration_delta_ms"`
}

// Check is the outcome of one expectation. Status is pass or fail.
type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Message  string `json:"message"`
}
