package localmedia

import (
	"math"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

// AnalysisMaxWindowMS and AnalysisMaxEvents bound a single analysis independently
// of the recording's length. A caller can explicitly analyze another window.
const AnalysisMaxWindowMS int64 = 30 * 60 * 1000
const AnalysisMaxEvents = 4096

// AnalysisInterval is a half-open interval on the original source timeline.
type AnalysisInterval struct {
	StartMS int64 `json:"start_ms"`
	EndMS   int64 `json:"end_ms"`
}

// AnalysisRange is an editable interval using the existing cutting range JSON.
type AnalysisRange = Range

// SilenceOptions measures the strongest channel's 10 ms RMS, without a downmix.
// Zero EndMS uses the source duration, subject to the 30 minute window limit.
// Zero ThresholdDB and MinSilenceMS use -35 dBFS and 500 ms respectively.
// Other zero values mean zero; the audio track is an absolute stream index.
type SilenceOptions struct {
	StartMS      int64   `json:"start_ms"`
	EndMS        int64   `json:"end_ms"`
	AudioTrack   *int    `json:"audio_track,omitempty"`
	ThresholdDB  float64 `json:"threshold_db"`
	MinSilenceMS int64   `json:"min_silence_ms"`
	KeepBeforeMS int64   `json:"keep_before_ms"`
	KeepAfterMS  int64   `json:"keep_after_ms"`
	MinClipMS    int64   `json:"min_clip_ms"`
}

// SilenceResult is a suggestion, never an executed edit. EndMS is requested;
// AnalyzedEndMS is actual audio coverage. Uncovered source tails are not silence
// and are not silently included in Keep. Arrays are non-nil, even when empty.
type SilenceResult struct {
	Version       int                `json:"version"`
	StartMS       int64              `json:"start_ms"`
	EndMS         int64              `json:"end_ms"`
	AnalyzedEndMS int64              `json:"analyzed_end_ms"`
	AudioTrack    int                `json:"audio_track"`
	Silence       []AnalysisInterval `json:"silence"`
	Keep          []AnalysisRange    `json:"keep"`
}

// VideoAnalysisOptions uses FFmpeg's scene score and blackdetect luma threshold.
// Zero thresholds use 0.3, 0.1 and 0.98; zero MinBlackMS uses 500 ms. Scene scores
// are visual discontinuities, not semantic chapter or interesting-moment scores.
type VideoAnalysisOptions struct {
	StartMS             int64   `json:"start_ms"`
	EndMS               int64   `json:"end_ms"`
	SceneThreshold      float64 `json:"scene_threshold"`
	BlackPixelThreshold float64 `json:"black_pixel_threshold"`
	BlackPictureRatio   float64 `json:"black_picture_ratio"`
	MinBlackMS          int64   `json:"min_black_ms"`
}

type SceneChange struct {
	AtMS  int64   `json:"at_ms"`
	Score float64 `json:"score"`
}

// VideoAnalysisResult reports actual decoded coverage. At end of file a black
// interval ends at the last observed frame timestamp, a conservative bound: a
// container duration alone cannot prove how long the final frame was displayed.
type VideoAnalysisResult struct {
	Version       int                `json:"version"`
	StartMS       int64              `json:"start_ms"`
	EndMS         int64              `json:"end_ms"`
	AnalyzedEndMS int64              `json:"analyzed_end_ms"`
	VideoTrack    int                `json:"video_track"`
	Scenes        []SceneChange      `json:"scenes"`
	Black         []AnalysisInterval `json:"black"`
}

func analysisArgument(message string) error {
	return &mediaerror.Problem{Code: "invalid_argument", Message: message}
}

func analysisLimit(message string) error {
	return &mediaerror.Problem{Code: "output_limit", Message: message}
}

func analysisMalformed() error {
	return &mediaerror.Problem{Code: "unsupported_stream", Message: "Media analysis returned incomplete or invalid decoded data"}
}

func analysisWindow(start, end, duration int64) (int64, int64, error) {
	if end == 0 {
		end = duration
	}
	if start < 0 || end <= start || end > duration || end-start > AnalysisMaxWindowMS {
		return 0, 0, analysisArgument("analysis requires a source interval of at most 30 minutes")
	}
	return start, end, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func silenceOptions(o SilenceOptions) (SilenceOptions, error) {
	if o.ThresholdDB == 0 {
		o.ThresholdDB = -35
	}
	if o.MinSilenceMS == 0 {
		o.MinSilenceMS = 500
	}
	if !finite(o.ThresholdDB) || o.ThresholdDB < -90 || o.ThresholdDB > -1 || o.MinSilenceMS < 10 || o.MinSilenceMS > 60000 || o.KeepBeforeMS < 0 || o.KeepBeforeMS > 5000 || o.KeepAfterMS < 0 || o.KeepAfterMS > 5000 || o.MinClipMS < 0 || o.MinClipMS > 60000 || (o.AudioTrack != nil && *o.AudioTrack < 0) {
		return o, analysisArgument("use threshold_db -90..-1, min_silence_ms 10..60000, keep margins 0..5000 and min_clip_ms 0..60000")
	}
	return o, nil
}

func videoAnalysisOptions(o VideoAnalysisOptions) (VideoAnalysisOptions, error) {
	if o.SceneThreshold == 0 {
		o.SceneThreshold = 0.3
	}
	if o.BlackPixelThreshold == 0 {
		o.BlackPixelThreshold = 0.1
	}
	if o.BlackPictureRatio == 0 {
		o.BlackPictureRatio = 0.98
	}
	if o.MinBlackMS == 0 {
		o.MinBlackMS = 500
	}
	if !finite(o.SceneThreshold) || !finite(o.BlackPixelThreshold) || !finite(o.BlackPictureRatio) || o.SceneThreshold < 0.001 || o.SceneThreshold > 1 || o.BlackPixelThreshold < 0 || o.BlackPixelThreshold > 1 || o.BlackPictureRatio <= 0 || o.BlackPictureRatio > 1 || o.MinBlackMS < 1 || o.MinBlackMS > 60000 {
		return o, analysisArgument("use scene_threshold 0.001..1, black_pixel_threshold 0..1, black_picture_ratio above 0 through 1, and min_black_ms 1..60000")
	}
	return o, nil
}

// KeepAfterSilence applies explicit margins and minimum clip length to an
// already measured timeline. Short retained clips are omitted only when the
// caller requested MinClipMS; no result is truncated to fit a cutting plan.
func KeepAfterSilence(start, end int64, silence []AnalysisInterval, before, after, minimum int64) []Range {
	keep := make([]Range, 0, len(silence)+1)
	cursor := start
	appendKeep := func(until int64) {
		if until > cursor && until-cursor >= minimum {
			keep = append(keep, Range{StartMS: cursor, EndMS: until})
		}
	}
	for _, interval := range silence {
		// KeepAfter preserves speech following its preceding audible segment;
		// KeepBefore preserves audio leading into the following audible segment.
		removeFrom := min(end, max(start, interval.StartMS+after))
		removeUntil := min(end, max(start, interval.EndMS-before))
		if interval.StartMS <= start {
			removeFrom = start
		}
		if interval.EndMS >= end {
			removeUntil = end
		}
		if removeUntil <= removeFrom {
			continue
		}
		appendKeep(removeFrom)
		cursor = max(cursor, removeUntil)
	}
	appendKeep(end)
	return keep
}
