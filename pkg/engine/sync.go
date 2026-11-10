package engine

import (
	"context"
	"errors"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type SyncOptions = localmedia.SyncOptions
type SyncResult = localmedia.SyncResult
type SyncInput = localmedia.SyncInput
type SyncSource = localmedia.SyncSource
type SyncPlacement = localmedia.SyncPlacement
type RepeatsOptions = localmedia.RepeatsOptions
type RepeatsResult = localmedia.RepeatsResult
type Repeat = localmedia.Repeat

// SyncAudio places a reference recording inside 1 through 16 inputs by sound:
// input_ms = ref_ms + offset_ms on the original timelines. It decodes the
// default audio stream of each recording, at most 30 minutes per window, and
// never writes media. Recordings cut from one master align to the sample; a
// remix that keeps the vocal aligns on attacks within a few milliseconds.
//
// A wrong offset is worse than a refusal. When any input has no placement, or
// several equally plausible ones without SyncOptions.All, the complete result
// is returned together with an Error whose Code is CodeNoMatch; each input's
// Status tells which. Inspection uses InspectTimeout; the whole operation uses
// ExportTimeout.
func (e *Engine) SyncAudio(ctx context.Context, reference string, inputs []string, options SyncOptions) (result SyncResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	config := e.config
	config.SourceTimeout = e.inspectTimeout
	result, err = localmedia.SyncAudio(ctx, config, reference, inputs, options)
	if err != nil {
		return SyncResult{}, err
	}
	for _, input := range result.Inputs {
		if input.Status != "match" {
			return result, &Error{Code: CodeNoMatch, Err: errors.New("no single confident placement for " + input.Path + ": " + input.Status)}
		}
	}
	return result, nil
}

// AnalyzeRepeats reports material that returns later in one recording, such as
// a chorus, with the accuracy of SyncAudio. An empty list is a valid answer.
// Only repeats nearly as alike as the strongest one are listed.
func (e *Engine) AnalyzeRepeats(ctx context.Context, path string, options RepeatsOptions) (result RepeatsResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	config := e.config
	config.SourceTimeout = e.inspectTimeout
	return localmedia.AnalyzeRepeats(ctx, config, path, options)
}
