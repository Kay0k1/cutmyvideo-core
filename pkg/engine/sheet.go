package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type SheetOptions = localmedia.SheetOptions
type SheetResult = localmedia.SheetResult
type SheetPage = localmedia.SheetPage
type SheetCell = localmedia.SheetCell
type SheetPair = localmedia.SheetPair

// GenerateSheet publishes a contact sheet: frames of local recordings, or
// supplied JPEG/PNG images, laid out on a labelled grid with an index that
// maps every cell to its source time. It never modifies an input.
//
// A cell shows the frame a player displays at its source time: the frame with
// the greatest presentation timestamp at or before that time, on the same
// source clock as Inspect and Export. FrameTimeMS reports the frame shown.
//
// A sheet with more cells than cols x max_rows becomes several pages named
// after output with -001, -002, ... before the suffix; a single page is
// exactly output. Existing files, including symlinks and output itself, are
// preserved and reported as ErrOutputExists before any frame is decoded. If
// exclusive publication fails after an earlier page was published, the
// returned Pages identifies the completed files.
//
// One request is limited to 2048 cells, 256 pages and 16777216 pixels per
// page. Inspection uses InspectTimeout; the whole operation uses
// ExportTimeout, and a stalled tool cannot outlive it.
func (e *Engine) GenerateSheet(ctx context.Context, output string, options SheetOptions) (result SheetResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	o, err := localmedia.NormalizeSheetOptions(options)
	if err != nil {
		return result, err
	}
	if output == "" {
		return result, invalidArgument(errors.New("sheet output path is required"))
	}
	extension := filepath.Ext(output)
	format := map[string]string{".jpg": "jpeg", ".jpeg": "jpeg", ".png": "png"}[strings.ToLower(extension)]
	if o.Format == "" {
		o.Format = format
	}
	if format == "" || o.Format != format {
		return result, invalidArgument(errors.New("sheet filename must end in .jpg, .jpeg or .png and match its image format"))
	}
	for _, paths := range [][]string{o.Inputs, o.Images} {
		for i := range paths {
			if paths[i], err = filepath.Abs(paths[i]); err != nil {
				return result, err
			}
		}
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	occupied := func(path string) error {
		if _, statErr := os.Lstat(path); statErr == nil {
			return ErrOutputExists
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		return nil
	}
	if err = occupied(output); err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(output), ".cutmy-sheet-")
	if err != nil {
		return result, fmt.Errorf("%w: create output workspace: %w", fsdurable.ErrPublicationFailed, err)
	}
	defer os.RemoveAll(dir)
	if err = e.preflight(dir, filepath.Dir(output)); err != nil {
		return result, err
	}
	names := []string{output}
	config := e.config
	config.SourceTimeout = e.inspectTimeout
	staged, err := localmedia.GenerateSheet(ctx, config, dir, o, func(pages int) error {
		if pages == 1 {
			return nil
		}
		names = names[:0]
		for page := 1; page <= pages; page++ {
			name := fmt.Sprintf("%s-%03d%s", strings.TrimSuffix(output, extension), page, extension)
			if err := occupied(name); err != nil {
				return err
			}
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result, result.Pages = staged, make([]SheetPage, 0, len(staged.Pages))
	for i, page := range staged.Pages {
		info, publishErr := e.publish(ctx, page.Path, names[i])
		if publishErr != nil {
			if errors.Is(publishErr, os.ErrExist) && !errors.Is(publishErr, fsdurable.ErrPublicationUncertain) {
				publishErr = errors.Join(ErrOutputExists, publishErr)
			}
			return result, publishErr
		}
		page.Path, page.SizeBytes = names[i], info.Size()
		result.Pages = append(result.Pages, page)
	}
	return result, nil
}
