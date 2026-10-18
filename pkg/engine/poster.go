package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type PosterOptions = localmedia.PosterOptions
type PosterResult = localmedia.PosterResult

// GeneratePoster publishes one JPEG/PNG frame at a finished video's output
// timestamp. Existing destinations, including symlinks, remain untouched.
func (e *Engine) GeneratePoster(ctx context.Context, input, output string, options PosterOptions) (result PosterResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	o, err := localmedia.NormalizePosterOptions(options)
	if err != nil {
		return result, invalidArgument(err)
	}
	if input == "" || output == "" {
		return result, invalidArgument(errors.New("input and output paths are required"))
	}
	ext := strings.ToLower(filepath.Ext(output))
	if o.Format == "jpeg" && ext != ".jpg" && ext != ".jpeg" || o.Format == "png" && ext != ".png" {
		return result, invalidArgument(errors.New("poster filename must match its image format"))
	}
	input, err = filepath.Abs(input)
	if err != nil {
		return result, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return result, err
	}
	if _, err = os.Lstat(output); err == nil {
		return result, ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(output), ".cutmy-poster-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	if err = e.preflight(dir, filepath.Dir(output)); err != nil {
		return result, err
	}
	result, err = localmedia.GeneratePoster(ctx, e.config, input, filepath.Join(dir, "poster"+ext), o)
	if err != nil {
		if errors.Is(err, localmedia.ErrInvalidPoster) {
			return result, invalidArgument(err)
		}
		if errors.Is(err, localmedia.ErrPosterTooLarge) {
			return result, &Error{Code: CodeOutputLimit, Err: err}
		}
		return result, err
	}
	if _, err = e.publish(ctx, result.Path, output); err != nil {
		if errors.Is(err, os.ErrExist) {
			return PosterResult{}, errors.Join(ErrOutputExists, err)
		}
		return PosterResult{}, err
	}
	result.Path = output
	return result, nil
}
