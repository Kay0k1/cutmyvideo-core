package engine

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type LoudnessOptions = edit.LoudnessOptions
type LoudnessResult = edit.LoudnessResult

// AnalyzeLoudness measures one selected audio stream without rendering or
// changing the source. Its deadline includes probing and the complete decode.
func (e *Engine) AnalyzeLoudness(ctx context.Context, input string, options LoudnessOptions) (result LoudnessResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	if input == "" {
		return result, invalidArgument(errors.New("input path is required"))
	}
	input, err = filepath.Abs(input)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.inspectTimeout)
	defer cancel()
	return localmedia.AnalyzeLoudness(ctx, e.config, input, options)
}
