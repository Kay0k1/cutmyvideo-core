package localmedia

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// GenerateSheet renders contact-sheet pages into dir and returns their index.
// Page paths in the result name the staged files; callers publish them.
// check, when set, sees the page count before any frame is decoded, so a
// caller can refuse occupied output names early.
//
// Frame rule: a cell shows the frame a player displays at the requested
// source time, the frame with the greatest presentation timestamp at or
// before it. Time zero is the container's start, as for every other source
// time in this package. A time before the first video frame shows that first
// frame, and a time after the last one shows the last; FrameTimeMS reports
// the frame actually shown.
//
// Frames reach this process already scaled to their tile as raw RGB. The
// grid, the captions and the image encoding use only the standard library.
func GenerateSheet(ctx context.Context, c Config, dir string, options SheetOptions, check func(pages int) error) (SheetResult, error) {
	o, err := NormalizeSheetOptions(options)
	if err != nil {
		return SheetResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return SheetResult{}, err
	}
	if o.Format == "" {
		o.Format = "jpeg"
	}
	if o.Labels[0] == sheetLabelNone {
		o.Labels = []string{}
	}
	if len(o.Images) != 0 {
		return generateImageSheet(ctx, c, dir, o, check)
	}
	return generateFrameSheet(ctx, c, dir, o, check)
}

type sheetCellPlan struct {
	source *sheetSource
	timeMS int64
}

func newSheetResult(o SheetOptions, mode string, l sheetLayout) SheetResult {
	return SheetResult{Version: 1, Mode: mode, Format: o.Format, Columns: l.columns, TileWidth: l.tileW, TileHeight: l.tileH, LabelHeight: l.labelH,
		Labels: o.Labels, SSIMThreshold: o.SSIMThreshold, Pages: make([]SheetPage, 0, l.pages()), Cells: make([]SheetCell, 0, l.cells),
		Pairs: make([]SheetPair, 0), BelowThreshold: make([]SheetPair, 0)}
}

func sheetPagePath(dir string, page int, format string) string {
	extension := "jpg"
	if format == "png" {
		extension = "png"
	}
	return filepath.Join(dir, fmt.Sprintf("page-%03d.%s", page+1, extension))
}

func sheetPageLimit(c Config) int64 {
	if c.MaxOutputBytes > 0 {
		return min(SheetMaxPageBytes, c.MaxOutputBytes)
	}
	return SheetMaxPageBytes
}

// sheetStartUS is the container start time in microseconds, the origin every
// other source time in this package already uses.
func sheetStartUS(value string) int64 {
	start, err := strconv.ParseFloat(value, 64)
	if err != nil || !finite(start) || math.Abs(start) > 1e12 {
		return 0
	}
	return int64(math.Round(start * 1e6))
}

// sheetFrameTimeMS rounds a presentation time up to the first whole
// millisecond at which that frame is on screen.
func sheetFrameTimeMS(us int64) int64 {
	if us > 0 {
		return (us + 999) / 1000
	}
	return us / 1000
}

func generateFrameSheet(ctx context.Context, c Config, dir string, o SheetOptions, check func(int) error) (SheetResult, error) {
	threads := sheetThreads(o)
	sources := make([]*sheetSource, 0, len(o.Inputs))
	var tileH int
	for i, path := range o.Inputs {
		_, session, info, duration, err := analysisSource(ctx, c, path)
		if err != nil {
			return SheetResult{}, err
		}
		defer session.Close()
		video := info.selectedVideo()
		if video == nil {
			return SheetResult{}, analysisArgument("a contact sheet requires a playable video stream in every input")
		}
		width, height, err := videoDisplayGeometry(video)
		if err != nil {
			return SheetResult{}, err
		}
		if i == 0 {
			if tileH, err = sheetTileHeight(o.TileWidth, width, height); err != nil {
				return SheetResult{}, err
			}
		}
		source := &sheetSource{number: i + 1, path: session.Path, session: session, videoIndex: video.Index, duration: duration, startUS: sheetStartUS(info.Format.StartTime)}
		source.width, source.height = sheetFit(width, height, o.TileWidth, tileH)
		filter, hdr, err := mediainfo.ColorRenderFilter(video, fmt.Sprintf("scale=%d:%d:flags=bicubic,setsar=1", source.width, source.height))
		if err != nil {
			return SheetResult{}, mediaInfoError(err)
		}
		source.filter, source.hdr = filter, hdr
		sources = append(sources, source)
	}

	var cells []sheetCellPlan
	if o.Compare {
		// Both recordings are sampled at the same times, inside their overlap.
		start, end, err := sheetWindow(o, min(sources[0].duration, sources[1].duration))
		if err != nil {
			return SheetResult{}, err
		}
		times, err := sheetTimes(o, start, end, nil)
		if err != nil {
			return SheetResult{}, err
		}
		for _, at := range times {
			cells = append(cells, sheetCellPlan{sources[0], at}, sheetCellPlan{sources[1], at})
		}
	} else {
		for _, source := range sources {
			start, end, err := sheetWindow(o, source.duration)
			if err != nil {
				return SheetResult{}, err
			}
			var changes []int64
			if o.Scenes {
				if changes, start, end, err = sheetSceneChanges(ctx, c, source, o, start, end, threads); err != nil {
					return SheetResult{}, err
				}
			}
			times, err := sheetTimes(o, start, end, changes)
			if err != nil {
				return SheetResult{}, err
			}
			if len(cells)+len(times) > SheetMaxCells {
				return SheetResult{}, analysisLimit("a contact sheet holds at most 2048 cells; choose fewer frames or inputs")
			}
			for _, at := range times {
				cells = append(cells, sheetCellPlan{source, at})
			}
		}
	}
	if len(cells) > SheetMaxCells {
		return SheetResult{}, analysisLimit("a contact sheet holds at most 2048 cells; choose fewer frames or inputs")
	}

	layout := newSheetLayout(o, tileH, len(cells))
	if err := layout.check(); err != nil {
		return SheetResult{}, err
	}
	if check != nil {
		if err := check(layout.pages()); err != nil {
			return SheetResult{}, err
		}
	}
	mode := sheetModeFrames
	if o.Compare {
		mode = sheetModeCompare
	}
	result := newSheetResult(o, mode, layout)
	capacity := layout.labelCapacity()
	for page := 0; page < layout.pages(); page++ {
		first, count := page*layout.perPage(), layout.pageCells(page)
		targets, err := sheetPageFrames(ctx, c, sources, cells[first:first+count], threads)
		if err != nil {
			return SheetResult{}, err
		}
		width, height := layout.pageSize(page)
		canvas := newSheetCanvas(width, height)
		for i, cell := range cells[first : first+count] {
			_, _, _, x, y := layout.position(first + i)
			drawSheetTile(canvas, x, y, layout.tileW, layout.tileH, targets[cell].tile)
		}
		var ssim float64
		for i, cell := range cells[first : first+count] {
			index := first + i
			_, column, row, x, y := layout.position(index)
			var suffix string
			flagged := false
			if o.Compare {
				if index%2 == 0 {
					// A pair shares a row: read both tiles back from the page.
					_, _, _, otherX, otherY := layout.position(index + 1)
					ssim = sheetSSIM(sheetLuma(canvas, x, y, layout.tileW, layout.tileH), sheetLuma(canvas, otherX, otherY, layout.tileW, layout.tileH), layout.tileW, layout.tileH)
				} else {
					suffix = "ssim " + strconv.FormatFloat(ssim, 'f', 3, 64)
					result.Pairs = append(result.Pairs, SheetPair{Index: index/2 + 1, TimeMS: cell.timeMS, SSIM: ssim, Cells: [2]int{index, index + 1}})
					if ssim < o.SSIMThreshold {
						result.BelowThreshold = append(result.BelowThreshold, result.Pairs[len(result.Pairs)-1])
					}
				}
				flagged = ssim < o.SSIMThreshold
			}
			timeMS, frameMS := cell.timeMS, sheetFrameTimeMS(targets[cell].frameUS)
			label := sheetLabel(o.Labels, index+1, cell.source.path, &timeMS, suffix, capacity)
			drawSheetLabel(canvas, layout, x, y, label, flagged)
			result.Cells = append(result.Cells, SheetCell{Index: index + 1, Page: page + 1, Column: column, Row: row, X: x, Y: y, Width: layout.tileW, Height: layout.tileH,
				Input: cell.source.number, Path: cell.source.path, TimeMS: &timeMS, FrameTimeMS: &frameMS, Label: label})
		}
		path := sheetPagePath(dir, page, o.Format)
		size, err := writeSheetPage(path, canvas, o.Format, sheetPageLimit(c))
		if err != nil {
			return SheetResult{}, err
		}
		result.Pages = append(result.Pages, SheetPage{Page: page + 1, Path: path, Width: width, Height: height, SizeBytes: size})
		if err = ctx.Err(); err != nil {
			return SheetResult{}, err
		}
	}
	for _, source := range sources {
		if err := analysisUnchanged(source.session); err != nil {
			return SheetResult{}, err
		}
	}
	return result, nil
}

// sheetPageFrames decodes every distinct source time on one page. Repeated
// times share a frame, and nearby times share a decoder pass.
func sheetPageFrames(ctx context.Context, c Config, sources []*sheetSource, cells []sheetCellPlan, threads int) (map[sheetCellPlan]*sheetTarget, error) {
	targets := make(map[sheetCellPlan]*sheetTarget, len(cells))
	bySource := make(map[*sheetSource][]*sheetTarget, len(sources))
	for _, cell := range cells {
		if targets[cell] == nil {
			targets[cell] = &sheetTarget{timeMS: cell.timeMS}
			bySource[cell.source] = append(bySource[cell.source], targets[cell])
		}
	}
	type job struct {
		source  *sheetSource
		targets []*sheetTarget
	}
	var jobs []job
	limit := min(sheetRunMaxTargets, max(4, (len(targets)+threads-1)/threads))
	for _, source := range sources {
		list := bySource[source]
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].timeMS < list[j].timeMS })
		for _, run := range sheetRuns(list, source.mergeGap(ctx, c, list), limit) {
			jobs = append(jobs, job{source, run})
		}
	}
	// Idle workers become decoder threads, so one dense pass still uses the
	// whole budget and many passes never oversubscribe it.
	decoderThreads := max(1, threads/min(threads, len(jobs)))
	err := sheetParallel(ctx, threads, len(jobs), func(ctx context.Context, i int) error {
		return jobs[i].source.resolve(ctx, c, decoderThreads, jobs[i].targets)
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return targets, err
}

// sheetSceneChanges returns ascending scene changes for one input and the
// interval they describe: a supplied analysis narrows the requested range to
// what it measured; otherwise the range is analysed in bounded windows.
func sheetSceneChanges(ctx context.Context, c Config, source *sheetSource, o SheetOptions, start, end int64, threads int) ([]int64, int64, int64, error) {
	var changes []int64
	if analysis := o.SceneAnalysis; analysis != nil {
		if analysis.Version != 1 || analysis.StartMS < 0 || analysis.EndMS <= analysis.StartMS || analysis.EndMS > source.duration {
			return nil, 0, 0, analysisArgument("scene_analysis must be a version 1 video analysis of this input")
		}
		start, end = max(start, analysis.StartMS), min(end, analysis.EndMS)
		if start >= end {
			return nil, 0, 0, analysisArgument("scene_analysis does not cover the requested sheet range")
		}
		previous := analysis.StartMS
		for _, scene := range analysis.Scenes {
			if scene.AtMS < previous || scene.AtMS > analysis.EndMS {
				return nil, 0, 0, analysisArgument("scene_analysis scenes must be ascending inside its interval")
			}
			changes, previous = append(changes, scene.AtMS), scene.AtMS
		}
		return changes, start, end, nil
	}
	config := c
	config.FFmpegThreads = threads
	for from := start; from < end; from += AnalysisMaxWindowMS {
		analysis, err := AnalyzeVideo(ctx, config, source.path, VideoAnalysisOptions{StartMS: from, EndMS: min(end, from+AnalysisMaxWindowMS)})
		if err != nil {
			return nil, 0, 0, err
		}
		for _, scene := range analysis.Scenes {
			changes = append(changes, scene.AtMS)
		}
	}
	return changes, start, end, nil
}

func generateImageSheet(ctx context.Context, c Config, dir string, o SheetOptions, check func(int) error) (SheetResult, error) {
	paths, err := sheetImagePaths(o.Images)
	if err != nil {
		return SheetResult{}, err
	}
	width, height, err := sheetImageSize(paths[0])
	if err != nil {
		return SheetResult{}, err
	}
	tileH, err := sheetTileHeight(o.TileWidth, width, height)
	if err != nil {
		return SheetResult{}, err
	}
	layout := newSheetLayout(o, tileH, len(paths))
	if err = layout.check(); err != nil {
		return SheetResult{}, err
	}
	if check != nil {
		if err = check(layout.pages()); err != nil {
			return SheetResult{}, err
		}
	}
	result := newSheetResult(o, sheetModeImages, layout)
	capacity, budget := layout.labelCapacity(), newSheetPixelBudget()
	for page := 0; page < layout.pages(); page++ {
		first, count := page*layout.perPage(), layout.pageCells(page)
		tiles := make([]sheetTile, count)
		err = sheetParallel(ctx, sheetThreads(o), count, func(ctx context.Context, i int) error {
			tile, loadErr := loadSheetImage(ctx, paths[first+i], layout.tileW, layout.tileH, budget)
			tiles[i] = tile
			return loadErr
		})
		if ctx.Err() != nil {
			return SheetResult{}, ctx.Err()
		}
		if err != nil {
			return SheetResult{}, err
		}
		pageW, pageH := layout.pageSize(page)
		canvas := newSheetCanvas(pageW, pageH)
		for i, tile := range tiles {
			index := first + i
			_, column, row, x, y := layout.position(index)
			drawSheetTile(canvas, x, y, layout.tileW, layout.tileH, tile)
			label := sheetLabel(o.Labels, index+1, paths[index], nil, "", capacity)
			drawSheetLabel(canvas, layout, x, y, label, false)
			result.Cells = append(result.Cells, SheetCell{Index: index + 1, Page: page + 1, Column: column, Row: row, X: x, Y: y, Width: layout.tileW, Height: layout.tileH,
				Input: index + 1, Path: paths[index], Label: label})
		}
		path := sheetPagePath(dir, page, o.Format)
		size, err := writeSheetPage(path, canvas, o.Format, sheetPageLimit(c))
		if err != nil {
			return SheetResult{}, err
		}
		result.Pages = append(result.Pages, SheetPage{Page: page + 1, Path: path, Width: pageW, Height: pageH, SizeBytes: size})
	}
	return result, nil
}
