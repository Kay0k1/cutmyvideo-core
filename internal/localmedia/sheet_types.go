package localmedia

import (
	"runtime"
	"slices"
)

// Sheet limits bound one request independently of the recordings' length.
const SheetMaxCells = 2048
const SheetMaxPages = 256
const SheetMaxInputs = 64
const SheetMaxPagePixels = 16_777_216
const SheetMaxPageBytes int64 = 64 << 20
const SheetDefaultCount = 24

const sheetDefaultColumns = 8
const sheetDefaultTileWidth = 320
const sheetDefaultMaxRows = 6
const sheetDefaultSSIMThreshold = 0.95

// SheetOptions selects what a contact sheet shows and how it is laid out.
//
// Inputs are local recordings; Images are JPEG/PNG files or directories for
// a sheet without video; Compare places the same moments of exactly two
// Inputs side by side. Exactly one of EveryMS, Count, AtMS and Scenes chooses
// the moments; none of them means Count 24. StartMS and EndMS restrict the
// source interval, and a zero EndMS means the end of the recording.
//
// Scenes shows the middle of every scene. SceneAnalysis supplies a finished
// AnalyzeVideo result for the single input instead of detecting scenes again.
//
// Zero Columns, TileWidth and MaxRows use 8, 320 and 6. Labels accepts time,
// index and name in drawing order, or none alone; an empty list uses time for
// recordings and name for images. An empty Format follows the output suffix.
// Zero Threads uses the number of CPUs, at most 16. A zero SSIMThreshold in a
// comparison uses 0.95.
type SheetOptions struct {
	Inputs        []string             `json:"inputs,omitempty"`
	Images        []string             `json:"images,omitempty"`
	Compare       bool                 `json:"compare,omitempty"`
	StartMS       int64                `json:"start_ms,omitempty"`
	EndMS         int64                `json:"end_ms,omitempty"`
	EveryMS       int64                `json:"every_ms,omitempty"`
	Count         int                  `json:"count,omitempty"`
	AtMS          []int64              `json:"at_ms,omitempty"`
	Scenes        bool                 `json:"scenes,omitempty"`
	SceneAnalysis *VideoAnalysisResult `json:"scene_analysis,omitempty"`
	Columns       int                  `json:"cols,omitempty"`
	TileWidth     int                  `json:"tile_width,omitempty"`
	MaxRows       int                  `json:"max_rows,omitempty"`
	Labels        []string             `json:"label,omitempty"`
	Format        string               `json:"format,omitempty"`
	Threads       int                  `json:"threads,omitempty"`
	SSIMThreshold float64              `json:"ssim_threshold,omitempty"`
}

// SheetResult is the machine-readable index of the published pages. Index,
// Page and Input count from one, matching the drawn "#N" label and the page
// file suffix; Column and Row are zero-based grid positions. Every cell's
// X, Y, Width and Height locate its picture on its page; the label strip of
// LabelHeight pixels lies directly below and belongs to no cell rectangle.
// Arrays are non-nil, even when empty.
type SheetResult struct {
	Version        int         `json:"version"`
	Mode           string      `json:"mode"`
	Format         string      `json:"format"`
	Columns        int         `json:"cols"`
	TileWidth      int         `json:"tile_width"`
	TileHeight     int         `json:"tile_height"`
	LabelHeight    int         `json:"label_height"`
	Labels         []string    `json:"label"`
	SSIMThreshold  float64     `json:"ssim_threshold,omitempty"`
	Pages          []SheetPage `json:"pages"`
	Cells          []SheetCell `json:"cells"`
	Pairs          []SheetPair `json:"pairs"`
	BelowThreshold []SheetPair `json:"below_threshold"`
}

type SheetPage struct {
	Page      int    `json:"page"`
	Path      string `json:"path"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SizeBytes int64  `json:"size_bytes"`
}

// SheetCell maps one picture to its source. TimeMS is the requested source
// time; FrameTimeMS is the shown frame's presentation time rounded up to a
// whole millisecond, so requesting it again selects the same frame. Image
// sheets omit both.
type SheetCell struct {
	Index       int    `json:"index"`
	Page        int    `json:"page"`
	Column      int    `json:"col"`
	Row         int    `json:"row"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Input       int    `json:"input"`
	Path        string `json:"path"`
	TimeMS      *int64 `json:"time_ms,omitempty"`
	FrameTimeMS *int64 `json:"frame_time_ms,omitempty"`
	Label       string `json:"label"`
}

// SheetPair is one compared moment. SSIM is the mean structural similarity
// of the two tiles' luma, 1 for identical pictures. Cells holds the indices
// of the first and second recording's cell.
type SheetPair struct {
	Index  int     `json:"index"`
	TimeMS int64   `json:"time_ms"`
	SSIM   float64 `json:"ssim"`
	Cells  [2]int  `json:"cells"`
}

const (
	sheetModeFrames  = "frames"
	sheetModeImages  = "images"
	sheetModeCompare = "compare"
)

// NormalizeSheetOptions validates everything that does not need the files and
// fills documented defaults. It never aliases the caller's slices.
func NormalizeSheetOptions(o SheetOptions) (SheetOptions, error) {
	o.Inputs, o.Images, o.AtMS, o.Labels = slices.Clone(o.Inputs), slices.Clone(o.Images), slices.Clone(o.AtMS), slices.Clone(o.Labels)
	for _, path := range append(slices.Clone(o.Inputs), o.Images...) {
		if path == "" {
			return o, analysisArgument("sheet inputs and images require non-empty paths")
		}
	}
	selections := 0
	if o.EveryMS != 0 {
		selections++
	}
	if o.Count != 0 {
		selections++
	}
	if len(o.AtMS) != 0 {
		selections++
	}
	if o.Scenes {
		selections++
	}
	if selections > 1 {
		return o, analysisArgument("choose one of every_ms, count, at_ms and scenes")
	}
	if o.StartMS < 0 || o.EndMS < 0 || o.EndMS != 0 && o.EndMS <= o.StartMS || o.EveryMS < 0 || o.Count < 0 {
		return o, analysisArgument("sheet range, interval and count must be nonnegative, with the range end after its start")
	}
	if o.Count > SheetMaxCells || len(o.AtMS) > SheetMaxCells {
		return o, analysisLimit("a contact sheet holds at most 2048 cells; choose fewer frames")
	}
	for _, at := range o.AtMS {
		if at < 0 {
			return o, analysisArgument("sheet times must be nonnegative")
		}
	}
	if o.SceneAnalysis != nil && (!o.Scenes || len(o.Inputs) != 1 || o.Compare) {
		return o, analysisArgument("scene_analysis requires scenes and exactly one input")
	}
	switch {
	case len(o.Images) != 0:
		if len(o.Inputs) != 0 || o.Compare || selections != 0 || o.StartMS != 0 || o.EndMS != 0 {
			return o, analysisArgument("an image sheet accepts neither recordings nor a time selection")
		}
	case o.Compare:
		if len(o.Inputs) != 2 || o.Scenes {
			return o, analysisArgument("a comparison requires exactly two inputs and every_ms, count or at_ms")
		}
	case len(o.Inputs) == 0:
		return o, analysisArgument("a contact sheet requires inputs or images")
	case len(o.Inputs) > SheetMaxInputs:
		return o, analysisLimit("a contact sheet accepts at most 64 inputs")
	}
	if len(o.Images) == 0 && selections == 0 {
		o.Count = SheetDefaultCount
	}
	if o.Columns == 0 {
		o.Columns = sheetDefaultColumns
	}
	if o.TileWidth == 0 {
		o.TileWidth = sheetDefaultTileWidth
	}
	if o.MaxRows == 0 {
		o.MaxRows = sheetDefaultMaxRows
	}
	if o.Columns < 1 || o.Columns > 64 || o.MaxRows < 1 || o.MaxRows > 64 || o.TileWidth < 48 || o.TileWidth > 1920 {
		return o, analysisArgument("use cols 1..64, max_rows 1..64 and tile_width 48..1920")
	}
	if o.Compare && o.Columns%2 != 0 {
		return o, analysisArgument("a comparison needs an even cols so each pair shares a row")
	}
	if len(o.Labels) == 0 {
		o.Labels = []string{sheetLabelTime}
		if len(o.Images) != 0 {
			o.Labels = []string{sheetLabelName}
		}
	}
	for i, label := range o.Labels {
		known := label == sheetLabelTime || label == sheetLabelIndex || label == sheetLabelName || label == sheetLabelNone
		if !known || slices.Contains(o.Labels[:i], label) || label == sheetLabelNone && len(o.Labels) != 1 || label == sheetLabelTime && len(o.Images) != 0 {
			return o, analysisArgument("label accepts time, index and name once each, or none alone; image sheets have no time")
		}
	}
	if o.Format != "" && o.Format != "jpeg" && o.Format != "png" {
		return o, analysisArgument("sheet format must be jpeg or png")
	}
	if o.Threads < 0 || o.Threads > 32 {
		return o, analysisArgument("sheet threads must be 0 for automatic or 1..32")
	}
	if o.Compare {
		if o.SSIMThreshold == 0 {
			o.SSIMThreshold = sheetDefaultSSIMThreshold
		}
		if !finite(o.SSIMThreshold) || o.SSIMThreshold <= 0 || o.SSIMThreshold > 1 {
			return o, analysisArgument("ssim_threshold must be above 0 through 1")
		}
	} else if o.SSIMThreshold != 0 {
		return o, analysisArgument("ssim_threshold requires a comparison")
	}
	return o, nil
}

// sheetThreads is the whole operation's worker budget: every decoder process
// and its threads share it, so a sheet never oversubscribes the machine.
func sheetThreads(o SheetOptions) int {
	if o.Threads > 0 {
		return o.Threads
	}
	return max(1, min(runtime.NumCPU(), 16))
}
