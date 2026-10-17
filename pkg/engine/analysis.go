package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type AnalysisInterval = localmedia.AnalysisInterval
type AnalysisRange = localmedia.AnalysisRange
type SilenceOptions = localmedia.SilenceOptions
type SilenceResult = localmedia.SilenceResult
type VideoAnalysisOptions = localmedia.VideoAnalysisOptions
type VideoAnalysisResult = localmedia.VideoAnalysisResult
type SceneChange = localmedia.SceneChange

// AnalyzeSilence reports quiet energy and editable keep intervals. It never
// exports media or modifies the input. All channels of the selected absolute
// audio stream participate, including antiphase stereo. A single request is
// limited to 30 minutes, 256 million decoded samples and 4096 silence intervals.
// Inspection uses InspectTimeout; the whole operation uses ExportTimeout.
func (e *Engine) AnalyzeSilence(ctx context.Context, path string, options SilenceOptions) (result SilenceResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	config := e.config
	config.SourceTimeout = e.inspectTimeout
	return localmedia.AnalyzeSilence(ctx, config, path, options)
}

// AnalyzeVideo reports visual scene changes and black intervals without an
// export. Source dimensions retain the existing decoder limit; a single request
// is limited to 30 minutes, 180000 decoded frames, 64 MiB of streamed metadata
// and 4096 detected events. Coverage ends at the last observed frame timestamp.
func (e *Engine) AnalyzeVideo(ctx context.Context, path string, options VideoAnalysisOptions) (result VideoAnalysisResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	config := e.config
	config.SourceTimeout = e.inspectTimeout
	return localmedia.AnalyzeVideo(ctx, config, path, options)
}

// BuildSilencePlan turns the caller's reviewed Keep intervals into the existing
// version 1 Plan. It does not inspect, execute or create files. At most 128
// ordered, nonoverlapping ranges are accepted; an all-silent or uncovered
// result cannot silently become an empty export or discard an unknown tail.
// Edit Keep first, or analyze another window, when a suggestion needs changes.
// Unless NoAudio is explicitly requested, the analysis audio selection is kept.
func BuildSilencePlan(input string, result SilenceResult, options AdvancedOptions, outputMode string) (Plan, error) {
	bad := func(message string) (Plan, error) { return Plan{}, invalidArgument(errors.New(message)) }
	if strings.TrimSpace(input) == "" || result.Version != 1 || result.StartMS < 0 || result.EndMS <= result.StartMS || result.EndMS-result.StartMS > localmedia.AnalysisMaxWindowMS || result.AnalyzedEndMS <= result.StartMS || result.AnalyzedEndMS > result.EndMS || result.AudioTrack < 0 {
		return bad("a version 1 silence result with valid decoded coverage and a local input is required")
	}
	if result.AnalyzedEndMS != result.EndMS {
		return bad("audio does not cover the requested interval; review its uncovered tail before building a plan")
	}
	if len(result.Keep) == 0 || len(result.Keep) > 128 {
		return bad("a silence plan requires 1 through 128 reviewed keep intervals; results are never truncated")
	}
	if outputMode == "" {
		outputMode = "separate"
	}
	if outputMode != "joined" && outputMode != "separate" {
		return bad("output_mode must be separate or joined")
	}
	if options.NoAudio {
		if options.AudioTrack != nil {
			return bad("choose no_audio or an audio_track, not both")
		}
	} else {
		if options.AudioTrack != nil && *options.AudioTrack != result.AudioTrack {
			return bad("plan audio_track must match the analyzed stream")
		}
		index := result.AudioTrack
		options.AudioTrack = &index
	}
	plan := Plan{Version: 1, Input: input, Options: options, OutputMode: outputMode, Ranges: make([]NamedRange, 0, len(result.Keep))}
	previous := result.StartMS
	for i, interval := range result.Keep {
		if interval.StartMS < previous || interval.EndMS <= interval.StartMS || interval.EndMS > result.AnalyzedEndMS {
			return bad("keep intervals must be ordered, nonoverlapping and inside decoded coverage")
		}
		label := interval.Label
		if label == "" {
			label = fmt.Sprintf("speech-%03d", i+1)
		}
		plan.Ranges = append(plan.Ranges, NamedRange{Label: label, StartMS: interval.StartMS, EndMS: interval.EndMS})
		previous = interval.EndMS
	}
	return plan, nil
}
