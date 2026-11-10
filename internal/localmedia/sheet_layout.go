package localmedia

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

const (
	sheetLabelTime  = "time"
	sheetLabelIndex = "index"
	sheetLabelName  = "name"
	sheetLabelNone  = "none"
)

// sheetWindow resolves the requested interval against one recording.
func sheetWindow(o SheetOptions, duration int64) (int64, int64, error) {
	end := o.EndMS
	if end == 0 {
		end = duration
	}
	if o.StartMS >= end || end > duration {
		return 0, 0, analysisArgument("sheet range must lie inside the source duration")
	}
	return o.StartMS, end, nil
}

// sheetTimes lists the requested source times inside [start, end). Every
// starts at the interval's beginning; Count takes the middle of equal parts,
// so neither the first nor the last frame is a fade or a slate by design;
// explicit times keep the caller's order and may repeat.
func sheetTimes(o SheetOptions, start, end int64, scenes []int64) ([]int64, error) {
	switch {
	case o.Scenes:
		return sheetSceneMiddles(start, end, scenes), nil
	case len(o.AtMS) != 0:
		for _, at := range o.AtMS {
			if at < start || at >= end {
				return nil, analysisArgument(fmt.Sprintf("sheet time %d ms lies outside the source interval %d..%d ms", at, start, end))
			}
		}
		return append([]int64(nil), o.AtMS...), nil
	case o.EveryMS != 0:
		count := (end - start + o.EveryMS - 1) / o.EveryMS
		if count > SheetMaxCells {
			return nil, analysisLimit("a contact sheet holds at most 2048 cells; choose a longer interval or a shorter range")
		}
		times := make([]int64, 0, count)
		for at := start; at < end; at += o.EveryMS {
			times = append(times, at)
		}
		return times, nil
	default:
		times := make([]int64, 0, o.Count)
		span, parts := end-start, int64(o.Count)
		for i := int64(0); i < parts; i++ {
			times = append(times, start+(2*i+1)*span/(2*parts))
		}
		return times, nil
	}
}

// sheetSceneMiddles turns ascending scene changes into one time per scene:
// the middle of every interval between neighbouring changes inside the range.
func sheetSceneMiddles(start, end int64, changes []int64) []int64 {
	times := make([]int64, 0, len(changes)+1)
	from := start
	for _, at := range changes {
		if at <= from || at >= end {
			continue
		}
		times = append(times, from+(at-from)/2)
		from = at
	}
	return append(times, from+(end-from)/2)
}

// sheetLayout places equal tiles on a grid. A label strip below each tile
// keeps the caption readable on any picture without hiding part of it.
type sheetLayout struct {
	columns, maxRows   int
	tileW, tileH       int
	labelH, gap, scale int
	cells              int
}

func newSheetLayout(o SheetOptions, tileH, cells int) sheetLayout {
	l := sheetLayout{columns: min(o.Columns, cells), maxRows: o.MaxRows, tileW: o.TileWidth, tileH: tileH, cells: cells}
	l.scale = max(1, min(8, o.TileWidth/160))
	l.gap = 2 * l.scale
	if len(o.Labels) != 0 {
		l.labelH = (sheetGlyphHeight + 4) * l.scale
	}
	return l
}

func (l sheetLayout) perPage() int { return l.columns * l.maxRows }
func (l sheetLayout) pages() int   { return (l.cells + l.perPage() - 1) / l.perPage() }

func (l sheetLayout) pageCells(page int) int {
	return min(l.perPage(), l.cells-page*l.perPage())
}

func (l sheetLayout) pageSize(page int) (int, int) {
	rows := (l.pageCells(page) + l.columns - 1) / l.columns
	return l.gap + l.columns*(l.tileW+l.gap), l.gap + rows*(l.tileH+l.labelH+l.gap)
}

// position returns the zero-based page, column and row of a zero-based cell
// and its tile's top-left pixel on that page.
func (l sheetLayout) position(index int) (page, column, row, x, y int) {
	page = index / l.perPage()
	within := index % l.perPage()
	column, row = within%l.columns, within/l.columns
	return page, column, row, l.gap + column*(l.tileW+l.gap), l.gap + row*(l.tileH+l.labelH+l.gap)
}

// labelCapacity is the number of characters the strip under a tile holds.
func (l sheetLayout) labelCapacity() int {
	return max(0, (l.tileW-4*l.scale)/(sheetGlyphAdvance*l.scale))
}

// check refuses a grid whose largest page would exceed the image limits.
func (l sheetLayout) check() error {
	if l.pages() > SheetMaxPages {
		return analysisLimit("a contact sheet has at most 256 pages; raise cols or max_rows")
	}
	width, height := l.pageSize(0)
	if width > 65535 || height > 65535 || width > SheetMaxPagePixels/height {
		return analysisLimit("a sheet page exceeds 16777216 pixels; lower cols, max_rows or tile_width")
	}
	return nil
}

// sheetTileHeight follows the first input's displayed aspect ratio.
func sheetTileHeight(tileW, width, height int) (int, error) {
	if width <= 0 || height <= 0 {
		return 0, analysisMalformed()
	}
	tileH := math.Round(float64(tileW) * float64(height) / float64(width))
	if tileH > float64(SheetMaxPagePixels/tileW) {
		return 0, analysisLimit("a sheet page exceeds 16777216 pixels; lower tile_width")
	}
	return max(2, int(tileH)), nil
}

// sheetFit contains a picture in a tile without stretching it.
func sheetFit(width, height, tileW, tileH int) (int, int) {
	factor := min(float64(tileW)/float64(width), float64(tileH)/float64(height))
	return max(1, min(tileW, int(math.Round(float64(width)*factor)))), max(1, min(tileH, int(math.Round(float64(height)*factor))))
}

func sheetTimeLabel(ms int64) string {
	hours, minutes, seconds, millis := ms/3600000, ms/60000%60, ms/1000%60, ms%1000
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
	}
	return fmt.Sprintf("%02d:%02d.%03d", minutes, seconds, millis)
}

// sheetLabel joins the requested parts in order. A text wider than the tile
// first shortens the file name around its middle, then loses its tail.
func sheetLabel(kinds []string, index int, path string, timeMS *int64, suffix string, capacity int) string {
	parts := make([]string, 0, len(kinds)+1)
	name := -1
	for _, kind := range kinds {
		switch kind {
		case sheetLabelIndex:
			parts = append(parts, fmt.Sprintf("#%d", index))
		case sheetLabelTime:
			if timeMS != nil {
				parts = append(parts, sheetTimeLabel(*timeMS))
			}
		case sheetLabelName:
			name = len(parts)
			parts = append(parts, sheetASCII(filepath.Base(path)))
		}
	}
	if suffix != "" && len(parts) != 0 {
		parts = append(parts, suffix)
	}
	text := strings.Join(parts, " ")
	if len(text) > capacity && name >= 0 {
		budget := len(parts[name]) - (len(text) - capacity)
		if budget < 4 {
			parts = append(parts[:name], parts[name+1:]...)
		} else {
			tail := (budget - 1) / 2
			parts[name] = parts[name][:budget-1-tail] + "~" + parts[name][len(parts[name])-tail:]
		}
		text = strings.Join(parts, " ")
	}
	if len(text) > capacity {
		text = text[:capacity]
	}
	return text
}
