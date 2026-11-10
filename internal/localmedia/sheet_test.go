package localmedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

func TestSheetOptionsDefaultsAndRefusals(t *testing.T) {
	o, err := NormalizeSheetOptions(SheetOptions{Inputs: []string{"a.mp4"}})
	if err != nil || o.Count != SheetDefaultCount || o.Columns != 8 || o.TileWidth != 320 || o.MaxRows != 6 || fmt.Sprint(o.Labels) != "[time]" || o.SSIMThreshold != 0 {
		t.Fatalf("frame defaults: %+v %v", o, err)
	}
	o, err = NormalizeSheetOptions(SheetOptions{Images: []string{"frames"}})
	if err != nil || o.Count != 0 || fmt.Sprint(o.Labels) != "[name]" {
		t.Fatalf("image defaults: %+v %v", o, err)
	}
	o, err = NormalizeSheetOptions(SheetOptions{Inputs: []string{"a.mp4", "b.mp4"}, Compare: true, EveryMS: 500, Labels: []string{"none"}})
	if err != nil || o.SSIMThreshold != 0.95 || fmt.Sprint(o.Labels) != "[none]" || o.Count != 0 {
		t.Fatalf("comparison defaults: %+v %v", o, err)
	}
	// Callers may normalize before handing options on: twice changes nothing.
	if again, err := NormalizeSheetOptions(o); err != nil || fmt.Sprintf("%+v", again) != fmt.Sprintf("%+v", o) {
		t.Fatalf("normalization is not idempotent: %+v %v", again, err)
	}
	inputs := []string{"a.mp4"}
	original := SheetOptions{Inputs: inputs, AtMS: []int64{1}, Labels: []string{"index", "time"}}
	normalized, _ := NormalizeSheetOptions(original)
	normalized.Inputs[0], normalized.AtMS[0], normalized.Labels[0] = "changed", 9, "name"
	if inputs[0] != "a.mp4" || original.AtMS[0] != 1 || original.Labels[0] != "index" {
		t.Fatal("normalization aliases caller slices")
	}
	one := []string{"a.mp4"}
	for name, bad := range map[string]SheetOptions{
		"nothing":            {},
		"empty path":         {Inputs: []string{""}},
		"two selections":     {Inputs: one, Count: 3, EveryMS: 1000},
		"scenes and at":      {Inputs: one, Scenes: true, AtMS: []int64{1}},
		"negative time":      {Inputs: one, AtMS: []int64{-1}},
		"negative interval":  {Inputs: one, EveryMS: -1},
		"reversed range":     {Inputs: one, StartMS: 2000, EndMS: 1000},
		"images and inputs":  {Inputs: one, Images: one},
		"images with time":   {Images: one, Count: 2},
		"images with range":  {Images: one, EndMS: 1000},
		"image time label":   {Images: one, Labels: []string{"time"}},
		"compare one":        {Inputs: one, Compare: true},
		"compare three":      {Inputs: []string{"a", "b", "c"}, Compare: true},
		"compare scenes":     {Inputs: []string{"a", "b"}, Compare: true, Scenes: true},
		"compare odd cols":   {Inputs: []string{"a", "b"}, Compare: true, Columns: 3},
		"threshold alone":    {Inputs: one, SSIMThreshold: 0.9},
		"threshold range":    {Inputs: []string{"a", "b"}, Compare: true, SSIMThreshold: 1.5},
		"analysis no scenes": {Inputs: one, SceneAnalysis: &VideoAnalysisResult{Version: 1}},
		"analysis two":       {Inputs: []string{"a", "b"}, Scenes: true, SceneAnalysis: &VideoAnalysisResult{Version: 1}},
		"cols":               {Inputs: one, Columns: 65},
		"rows":               {Inputs: one, MaxRows: -1},
		"tile":               {Inputs: one, TileWidth: 47},
		"label":              {Inputs: one, Labels: []string{"date"}},
		"label twice":        {Inputs: one, Labels: []string{"time", "time"}},
		"none with others":   {Inputs: one, Labels: []string{"none", "time"}},
		"format":             {Inputs: one, Format: "webp"},
		"threads":            {Inputs: one, Threads: 33},
	} {
		if _, err := NormalizeSheetOptions(bad); mediaerror.LocalErrorCode(err) != "invalid_argument" {
			t.Errorf("%s: accepted or misclassified: %v", name, err)
		}
	}
	for name, large := range map[string]SheetOptions{
		"count":  {Inputs: one, Count: SheetMaxCells + 1},
		"times":  {Inputs: one, AtMS: make([]int64, SheetMaxCells+1)},
		"inputs": {Inputs: make([]string, SheetMaxInputs+1)},
	} {
		for i := range large.Inputs {
			large.Inputs[i] = "a.mp4"
		}
		if _, err := NormalizeSheetOptions(large); mediaerror.LocalErrorCode(err) != "output_limit" {
			t.Errorf("%s: limit lost: %v", name, err)
		}
	}
	if sheetThreads(SheetOptions{Threads: 3}) != 3 || sheetThreads(SheetOptions{}) < 1 || sheetThreads(SheetOptions{}) > 16 {
		t.Fatal("thread budget")
	}
}

func TestSheetTimesFollowTheRequestedSelection(t *testing.T) {
	for _, tc := range []struct {
		options    SheetOptions
		start, end int64
		scenes     []int64
		want       string
	}{
		{SheetOptions{EveryMS: 2000}, 0, 6000, nil, "[0 2000 4000]"},
		{SheetOptions{EveryMS: 2000}, 500, 6001, nil, "[500 2500 4500]"},
		{SheetOptions{EveryMS: 2000}, 0, 1, nil, "[0]"},
		{SheetOptions{Count: 4}, 0, 8000, nil, "[1000 3000 5000 7000]"},
		{SheetOptions{Count: 3}, 1000, 2000, nil, "[1166 1500 1833]"},
		{SheetOptions{Count: 1}, 0, 1, nil, "[0]"},
		{SheetOptions{AtMS: []int64{5000, 0, 5000}}, 0, 6000, nil, "[5000 0 5000]"},
		{SheetOptions{Scenes: true}, 0, 6000, []int64{2000, 5000}, "[1000 3500 5500]"},
		{SheetOptions{Scenes: true}, 1000, 4000, []int64{500, 2000, 2000, 5000}, "[1500 3000]"},
		{SheetOptions{Scenes: true}, 0, 6000, nil, "[3000]"},
	} {
		got, err := sheetTimes(tc.options, tc.start, tc.end, tc.scenes)
		if err != nil || fmt.Sprint(got) != tc.want {
			t.Errorf("%+v in %d..%d: %v %v, want %s", tc.options, tc.start, tc.end, got, err, tc.want)
		}
		for _, at := range got {
			if at < tc.start || at >= tc.end {
				t.Errorf("%+v selected %d outside %d..%d", tc.options, at, tc.start, tc.end)
			}
		}
	}
	for _, at := range []int64{6000, 999, 7000} {
		if _, err := sheetTimes(SheetOptions{AtMS: []int64{at}}, 1000, 6000, nil); mediaerror.LocalErrorCode(err) != "invalid_argument" {
			t.Errorf("time %d outside the source accepted: %v", at, err)
		}
	}
	if _, err := sheetTimes(SheetOptions{EveryMS: 1}, 0, SheetMaxCells+1, nil); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("dense interval not bounded", err)
	}
	if times, err := sheetTimes(SheetOptions{EveryMS: 1}, 0, SheetMaxCells, nil); err != nil || len(times) != SheetMaxCells {
		t.Fatal("interval at the cell limit refused", len(times), err)
	}
	for _, window := range [][3]int64{{0, 0, 5000}, {1000, 5000, 5000}, {0, 5000, 5000}} {
		if start, end, err := sheetWindow(SheetOptions{StartMS: window[0], EndMS: window[1]}, window[2]); err != nil || start != window[0] || end != 5000 {
			t.Errorf("window %v: %d %d %v", window, start, end, err)
		}
	}
	for _, window := range [][3]int64{{5000, 0, 5000}, {0, 5001, 5000}, {6000, 7000, 5000}} {
		if _, _, err := sheetWindow(SheetOptions{StartMS: window[0], EndMS: window[1]}, window[2]); mediaerror.LocalErrorCode(err) != "invalid_argument" {
			t.Errorf("window %v outside the source accepted: %v", window, err)
		}
	}
}

func TestSheetLayoutKeepsCellsDisjointInsidePages(t *testing.T) {
	for _, tc := range []struct {
		options SheetOptions
		cells   int
		pages   int
		columns int
	}{
		{SheetOptions{Columns: 8, MaxRows: 6, TileWidth: 320, Labels: []string{"time"}}, 48, 1, 8},
		{SheetOptions{Columns: 8, MaxRows: 6, TileWidth: 320, Labels: []string{"time"}}, 49, 2, 8},
		{SheetOptions{Columns: 8, MaxRows: 6, TileWidth: 320, Labels: []string{}}, 3, 1, 3},
		{SheetOptions{Columns: 4, MaxRows: 2, TileWidth: 96, Labels: []string{"index"}}, 20, 3, 4},
		{SheetOptions{Columns: 1, MaxRows: 1, TileWidth: 48, Labels: []string{"index"}}, 5, 5, 1},
	} {
		l := newSheetLayout(tc.options, 180, tc.cells)
		if l.pages() != tc.pages || l.columns != tc.columns || l.check() != nil || (l.labelH == 0) != (len(tc.options.Labels) == 0) {
			t.Fatalf("%+v: pages=%d columns=%d label=%d", tc.options, l.pages(), l.columns, l.labelH)
		}
		seen := map[[3]int]bool{}
		var placed []image.Rectangle
		var pages []int
		for i := 0; i < tc.cells; i++ {
			page, column, row, x, y := l.position(i)
			width, height := l.pageSize(page)
			cell := image.Rect(x, y, x+l.tileW, y+l.tileH+l.labelH)
			if !cell.In(image.Rect(0, 0, width, height)) || column >= l.columns || row >= l.maxRows || seen[[3]int{page, column, row}] {
				t.Fatalf("cell %d misplaced: page=%d column=%d row=%d %v in %dx%d", i, page, column, row, cell, width, height)
			}
			seen[[3]int{page, column, row}] = true
			for j, other := range placed {
				if pages[j] == page && cell.Overlaps(other) {
					t.Fatalf("cells %d and %d overlap on page %d", i, j, page)
				}
			}
			placed, pages = append(placed, cell), append(pages, page)
		}
		last := l.pages() - 1
		if _, height := l.pageSize(last); height != l.gap+((l.pageCells(last)+l.columns-1)/l.columns)*(l.tileH+l.labelH+l.gap) {
			t.Fatal("last page keeps empty rows")
		}
	}
	if err := newSheetLayout(SheetOptions{Columns: 64, MaxRows: 64, TileWidth: 1920, Labels: []string{}}, 1080, 2048).check(); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("page pixel limit lost", err)
	}
	if err := newSheetLayout(SheetOptions{Columns: 1, MaxRows: 1, TileWidth: 48, Labels: []string{}}, 27, SheetMaxPages+1).check(); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("page count limit lost", err)
	}
	if _, err := sheetTileHeight(1920, 2, 4000); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("extreme proportions not bounded", err)
	}
	for _, fit := range [][6]int{{1920, 1080, 320, 180, 320, 180}, {1080, 1920, 320, 180, 101, 180}, {100, 100, 320, 180, 180, 180}, {4000, 10, 320, 180, 320, 1}, {16, 9, 320, 180, 320, 180}} {
		if width, height := sheetFit(fit[0], fit[1], fit[2], fit[3]); width != fit[4] || height != fit[5] {
			t.Errorf("fit %v: %dx%d", fit, width, height)
		}
	}
}

func TestSheetLabelsFitTheirTile(t *testing.T) {
	at := int64(3723004)
	for _, tc := range []struct {
		kinds    []string
		path     string
		suffix   string
		capacity int
		want     string
	}{
		{[]string{"time"}, "a.mp4", "", 26, "01:02:03.004"},
		{[]string{"index", "time", "name"}, "/clips/a.mp4", "", 26, "#7 01:02:03.004 a.mp4"},
		{[]string{"name", "index"}, "/clips/interview-camera-a.mp4", "", 16, "interv~-a.mp4 #7"},
		{[]string{"time", "name"}, "/clips/interview.mp4", "", 14, "01:02:03.004"},
		{[]string{"time"}, "a.mp4", "ssim 0.912", 26, "01:02:03.004 ssim 0.912"},
		{[]string{"time"}, "a.mp4", "", 5, "01:02"},
		{[]string{"name"}, "/clips/дубль 1.mp4", "", 26, "????? 1.mp4"},
		{[]string{}, "a.mp4", "ssim 1.000", 26, ""},
	} {
		if got := sheetLabel(tc.kinds, 7, tc.path, &at, tc.suffix, tc.capacity); got != tc.want || len(got) > tc.capacity {
			t.Errorf("%v %q capacity %d: %q, want %q", tc.kinds, tc.path, tc.capacity, got, tc.want)
		}
	}
	if got := sheetLabel([]string{"time", "name"}, 1, "photo.png", nil, "", 26); got != "photo.png" {
		t.Fatalf("image label: %q", got)
	}
	for ms, want := range map[int64]string{0: "00:00.000", 59999: "00:59.999", 3599999: "59:59.999", 3600000: "01:00:00.000", 90061001: "25:01:01.001"} {
		if got := sheetTimeLabel(ms); got != want {
			t.Errorf("time %d: %q, want %q", ms, got, want)
		}
	}
}

func TestSheetFontDrawsEveryPrintableGlyphInsideItsCell(t *testing.T) {
	for i, glyph := range sheetGlyphs {
		dots := 0
		for _, column := range glyph {
			if column&0x80 != 0 {
				t.Fatalf("glyph %q uses a row below the seven-row cell", rune(i+32))
			}
			for row := 0; row < sheetGlyphHeight; row++ {
				dots += int(column >> row & 1)
			}
		}
		if (dots == 0) != (i == 0) {
			t.Fatalf("glyph %q has %d dots", rune(i+32), dots)
		}
		for j := range i {
			if sheetGlyphs[j] == glyph {
				t.Fatalf("glyphs %q and %q are identical", rune(j+32), rune(i+32))
			}
		}
	}
	// An upright L: a full left column and a full bottom row, nothing else.
	const scale = 3
	img := image.NewRGBA(image.Rect(0, 0, 4*sheetGlyphAdvance*scale, sheetGlyphHeight*scale))
	drawSheetText(img, 0, 0, scale, "L", 255, 255, 255)
	for y := 0; y < sheetGlyphHeight*scale; y++ {
		for x := 0; x < sheetGlyphAdvance*scale; x++ {
			want := x < scale || y >= (sheetGlyphHeight-1)*scale && x < sheetGlyphWidth*scale
			if got := img.Pix[img.PixOffset(x, y)] == 255; got != want {
				t.Fatalf("L at %d,%d: lit=%v", x, y, got)
			}
		}
	}
	// Unsupported characters become '?', and drawing never leaves the image.
	other, question := image.NewRGBA(img.Bounds()), image.NewRGBA(img.Bounds())
	drawSheetText(other, 0, 0, scale, "\x01\xff", 255, 255, 255)
	drawSheetText(question, 0, 0, scale, "??", 255, 255, 255)
	if !bytes.Equal(other.Pix, question.Pix) || sheetASCII("кадр é\t1") != "???? ??1" {
		t.Fatal("unsupported characters were not replaced")
	}
	drawSheetText(img, -40, -5, scale, strings.Repeat("W", 40), 1, 2, 3)
}

func TestSheetSSIMSeparatesEqualAndDifferentPictures(t *testing.T) {
	const width, height = 64, 48
	base, noisy, inverted := make([]byte, width*height), make([]byte, width*height), make([]byte, width*height)
	seed := uint32(1)
	for i := range base {
		base[i] = byte(40 + (i%width)*3 + (i/width)*2)
		seed = seed*1664525 + 1013904223
		noisy[i] = byte(int(base[i]) + int(seed>>30) - 1)
		inverted[i] = 255 - base[i]
	}
	if got := sheetSSIM(base, base, width, height); got != 1 {
		t.Fatalf("identical planes: %v", got)
	}
	slight, opposite := sheetSSIM(base, noisy, width, height), sheetSSIM(base, inverted, width, height)
	if slight < 0.9 || slight >= 1 || opposite > 0.5 || opposite >= slight {
		t.Fatalf("ordering lost: slight=%v opposite=%v", slight, opposite)
	}
	if sheetSSIM(base, noisy, width, height) != sheetSSIM(noisy, base, width, height) {
		t.Fatal("similarity is not symmetric")
	}
	flat, bright := bytes.Repeat([]byte{16}, 12), bytes.Repeat([]byte{235}, 12)
	if sheetSSIM(flat, flat, 4, 3) != 1 || sheetSSIM(flat, bright, 4, 3) > 0.2 {
		t.Fatal("planes smaller than one window are not compared")
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	fillSheetRect(img, 0, 0, 4, 2, 255, 0, 0)
	fillSheetRect(img, 2, 0, 2, 2, 255, 255, 255)
	if luma := sheetLuma(img, 1, 0, 2, 2); fmt.Sprint(luma) != "[76 255 76 255]" {
		t.Fatalf("luma: %v", luma)
	}
}

// sheetFeed plays a decoder: timestamp lines on the diagnostic stream and raw
// frames on the output, each split at awkward boundaries.
func sheetFeed(t *testing.T, stream *sheetFrameStream, chunk int, frames []int64) error {
	t.Helper()
	info := &sheetInfoWriter{stream: stream}
	for i, pts := range frames {
		line := fmt.Sprintf("[Parsed_showinfo_4 @ 0x55d0] n:%4d pts:%7d pts_time:%g duration: 40000 fmt:rgb24 s:2x1\n[Parsed_showinfo_4 @ 0x55d0] color_range:pc\n", i, pts, float64(pts)/1e6)
		for len(line) > 0 {
			n := min(7, len(line))
			if _, err := info.Write([]byte(line[:n])); err != nil {
				t.Fatal(err)
			}
			line = line[n:]
		}
		frame := bytes.Repeat([]byte{byte(i + 1)}, stream.frameSize)
		for len(frame) > 0 {
			n := min(chunk, len(frame))
			if _, err := stream.Write(frame[:n]); err != nil {
				return err
			}
			frame = frame[n:]
		}
	}
	return nil
}

func TestSheetFrameStreamSelectsTheDisplayedFrame(t *testing.T) {
	source := &sheetSource{width: 2, height: 1, startUS: 1_400_000}
	// Frames 1..5 on a 40 ms grid after a container start of 1.4 s; frame 4 is
	// absent, so frame 3 stays on screen for 80 ms.
	frames := []int64{1_400_000, 1_440_000, 1_480_000, 1_560_000, 1_600_000}
	resolved := func(targets []*sheetTarget) string {
		var got []string
		for _, target := range targets {
			if !target.found {
				got = append(got, fmt.Sprintf("%d:-", target.timeMS))
				continue
			}
			if target.tile.width != 2 || target.tile.height != 1 || len(target.tile.rgb) != 6 {
				t.Fatalf("target %d has a malformed tile: %+v", target.timeMS, target.tile)
			}
			got = append(got, fmt.Sprintf("%d:%d@%d", target.timeMS, target.tile.rgb[0], target.frameUS))
		}
		return strings.Join(got, " ")
	}
	for _, chunk := range []int{1, 4, 6, 64} {
		// A windowed pass ends by itself. Its newest frame answers a target
		// only while it lies inside that target's seek window.
		targets := []*sheetTarget{{timeMS: 0}, {timeMS: 39}, {timeMS: 40}, {timeMS: 81}, {timeMS: 119}, {timeMS: 159}, {timeMS: 160}, {timeMS: 450}, {timeMS: 451}}
		stream := newSheetFrameStream(context.Background(), source, targets, true, false)
		if err := sheetFeed(t, stream, chunk, frames); err != nil {
			t.Fatal(err)
		}
		if err := stream.finish(); err != nil {
			t.Fatal(err)
		}
		if got, want := resolved(targets), "0:1@0 39:1@0 40:2@40000 81:3@80000 119:3@80000 159:3@80000 160:4@160000 450:5@200000 451:-"; got != want {
			t.Fatalf("chunk %d: %s, want %s", chunk, got, want)
		}
	}

	// Between two windows the decoder drops frames, so a frame from the
	// earlier window never answers the later target.
	targets := []*sheetTarget{{timeMS: 40}, {timeMS: 1000}}
	stream := newSheetFrameStream(context.Background(), source, targets, true, false)
	if err := sheetFeed(t, stream, 6, []int64{1_400_000, 1_440_000, 2_480_000}); err != nil || stream.finish() != nil {
		t.Fatal(err)
	}
	if got := resolved(targets); got != "40:2@40000 1000:-" {
		t.Fatal(got)
	}

	// A target before the first delivered frame stays unresolved for a retry,
	// unless this pass started at the beginning of the recording.
	targets = []*sheetTarget{{timeMS: 10}, {timeMS: 100}}
	stream = newSheetFrameStream(context.Background(), source, targets, true, false)
	if err := sheetFeed(t, stream, 6, []int64{1_440_000, 1_480_000}); err != nil || stream.finish() != nil {
		t.Fatal(err)
	}
	if got := resolved(targets); got != "10:- 100:2@80000" {
		t.Fatal(got)
	}
	targets = []*sheetTarget{{timeMS: 10}}
	stream = newSheetFrameStream(context.Background(), source, targets, false, true)
	if err := sheetFeed(t, stream, 6, []int64{1_440_000, 1_480_000}); !errors.Is(err, errSheetStop) || resolved(targets) != "10:1@40000" {
		t.Fatalf("first frame not used for an earlier time: %v %s", err, resolved(targets))
	}
	targets = []*sheetTarget{{timeMS: 10}}
	stream = newSheetFrameStream(context.Background(), source, targets, false, false)
	if err := sheetFeed(t, stream, 5, []int64{1_440_000}); !errors.Is(err, errSheetStop) || resolved(targets) != "10:-" {
		t.Fatalf("a later frame satisfied an earlier time without clamping: %v %s", err, resolved(targets))
	}

	// An open pass accepts a frame of any age, ends at the first frame after
	// its target and keeps the frame before it although buffers are reused.
	targets = []*sheetTarget{{timeMS: 50}}
	stream = newSheetFrameStream(context.Background(), source, targets, false, false)
	if err := sheetFeed(t, stream, 5, frames); !errors.Is(err, errSheetStop) || resolved(targets) != "50:2@40000" {
		t.Fatalf("open pass: %v %s", err, resolved(targets))
	}
	targets = []*sheetTarget{{timeMS: 9000}}
	stream = newSheetFrameStream(context.Background(), source, targets, false, false)
	if err := sheetFeed(t, stream, 6, frames); err != nil || stream.finish() != nil || resolved(targets) != "9000:5@200000" {
		t.Fatalf("open pass at the end of the recording: %v %s", err, resolved(targets))
	}

	// A resolved frame is never overwritten by the frames that follow it.
	targets = []*sheetTarget{{timeMS: 0}, {timeMS: 200}}
	stream = newSheetFrameStream(context.Background(), source, targets, true, false)
	if err := sheetFeed(t, stream, 6, frames); err != nil || stream.finish() != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(targets[0].tile.rgb, bytes.Repeat([]byte{1}, 6)) || !bytes.Equal(targets[1].tile.rgb, bytes.Repeat([]byte{5}, 6)) {
		t.Fatalf("frames were overwritten: %v %v", targets[0].tile.rgb, targets[1].tile.rgb)
	}

	if got, want := sheetWindows([]*sheetTarget{{timeMS: 0}, {timeMS: 100}, {timeMS: 350}, {timeMS: 2000}}, 1_400_000), "select='between(pts,1150000,1750000)+between(pts,3150000,3400000)+gt(pts,3400000)',trim=end_pts=3400001"; got != want {
		t.Fatalf("windows: %s, want %s", got, want)
	}
}

func TestSheetFrameStreamRefusesIncompleteOrUntimedFrames(t *testing.T) {
	source := &sheetSource{width: 2, height: 1}
	stream := newSheetFrameStream(context.Background(), source, []*sheetTarget{{timeMS: 0}}, true, false)
	if err := sheetFeed(t, stream, 6, []int64{0}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := stream.finish(); mediaerror.LocalErrorCode(err) != "unsupported_media" {
		t.Fatal("partial frame accepted", err)
	}
	stream = newSheetFrameStream(context.Background(), source, []*sheetTarget{{timeMS: 0}}, true, false)
	(&sheetInfoWriter{stream: stream}).Write([]byte("[Parsed_showinfo_1 @ 0x1] n:   0 pts:      0 pts_time:0\n"))
	if err := stream.finish(); mediaerror.LocalErrorCode(err) != "unsupported_media" {
		t.Fatal("timestamp without its frame accepted", err)
	}
	stream = newSheetFrameStream(context.Background(), source, []*sheetTarget{{timeMS: 0}}, false, false)
	(&sheetInfoWriter{stream: stream}).Write([]byte("[Parsed_showinfo_1 @ 0x1] n:   0 pts:NOPTS pts_time:NOPTS\n"))
	if _, err := stream.Write(make([]byte, 6)); mediaerror.LocalErrorCode(err) != "unsupported_media" {
		t.Fatal("frame without a timestamp accepted", err)
	}
	// A frame whose timestamp never arrives ends with the operation deadline
	// instead of blocking the decoder's output forever.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stream = newSheetFrameStream(ctx, source, []*sheetTarget{{timeMS: 0}}, false, false)
	started := time.Now()
	if _, err := stream.Write(make([]byte, 6)); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Fatal("untimed frame did not honour the deadline", err, time.Since(started))
	}
	for line, frame := range map[string]bool{
		"[Parsed_showinfo_3 @ 0x7f] n:  12 pts:3120000 pts_time:3.12    duration:  40000 fmt:rgb24": true,
		"[Parsed_showinfo_3 @ 0x7f] config in time_base: 1/1000000, frame_rate: 25/1":               false,
		"[Parsed_showinfo_3 @ 0x7f] color_range:pc color_space:gbr":                                 false,
		"[out#0/rawvideo @ 0x55] video:253KiB audio:0KiB":                                           false,
		"[libx264 @ 0x1] n: 3 pts: 4":                                                               false,
		"":                                                                                          false,
	} {
		pts, isFrame, err := parseSheetFrameInfo(line)
		if isFrame != frame || err != nil || frame && pts != 3120000 {
			t.Errorf("%q: pts=%d frame=%v err=%v", line, pts, isFrame, err)
		}
	}
	if pts, frame, err := parseSheetFrameInfo("[Parsed_showinfo_0 @ 0x1] n:   0 pts: -40000 pts_time:-0.04"); !frame || err != nil || pts != -40000 {
		t.Fatal("negative timestamp", pts, frame, err)
	}
	// Overlong diagnostics are skipped whole; the following line still parses.
	stream = newSheetFrameStream(context.Background(), source, nil, false, false)
	writer := &sheetInfoWriter{stream: stream}
	writer.Write([]byte(strings.Repeat("x", 5000) + "] n: 1 pts: 5 showinfo\n[Parsed_showinfo_0 @ 0x1] n:   0 pts:     77 pts_time:0\n"))
	if len(stream.pts) != 1 || stream.pts[0] != 77 {
		t.Fatalf("diagnostic lines: %v", stream.pts)
	}
	for us, want := range map[int64]int64{0: 0, 1: 1, 999: 1, 1000: 1, 33367: 34, 40000: 40, -1: 0, -1500: -1} {
		if got := sheetFrameTimeMS(us); got != want {
			t.Errorf("frame time %d us: %d ms, want %d", us, got, want)
		}
	}
	for value, want := range map[string]int64{"0.000000": 0, "1.400000": 1400000, "-0.023220": -23220, "": 0, "N/A": 0, "NaN": 0} {
		if got := sheetStartUS(value); got != want {
			t.Errorf("start %q: %d, want %d", value, got, want)
		}
	}
}

func TestSheetRunsShareNearbyTimesAndStayDivisible(t *testing.T) {
	times := func(runs [][]*sheetTarget) string {
		var out []string
		for _, run := range runs {
			var list []int64
			for _, target := range run {
				list = append(list, target.timeMS)
			}
			out = append(out, fmt.Sprint(list))
		}
		return strings.Join(out, " ")
	}
	targets := func(values ...int64) []*sheetTarget {
		var list []*sheetTarget
		for _, value := range values {
			list = append(list, &sheetTarget{timeMS: value})
		}
		return list
	}
	if got := times(sheetRuns(targets(0, 400, 1200, 2201, 9000, 9100), 1000, 8)); got != "[0 400 1200] [2201] [9000 9100]" {
		t.Fatal(got)
	}
	if got := times(sheetRuns(targets(0, 100, 200, 300, 400), 1000, 2)); got != "[0 100] [200 300] [400]" {
		t.Fatal(got)
	}
	if got := times(sheetRuns(targets(0, 100, 200), 0, 8)); got != "[0] [100] [200]" {
		t.Fatal(got)
	}
	if got := times(sheetRuns(targets(7), 1000, 8)); got != "[7]" {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		keyframes []int64
		want      int64
	}{
		{nil, sheetRunGapMax},
		{[]int64{0}, sheetRunGapMax},
		{[]int64{4000, 4000}, sheetRunGapMax},
		{[]int64{0, 10000}, sheetRunGapBase + 3333},
		{[]int64{0, 60000}, sheetRunGapMax},
		{[]int64{6000, 0, 4000, 2000}, sheetRunGapBase + 666},
		{[]int64{0, 40, 80, 120, 160}, sheetRunGapBase + 13},
		{[]int64{0, 1000, 1040, 3000, 5000}, sheetRunGapBase + 653},
	} {
		if got := sheetRunGap(tc.keyframes); got != tc.want {
			t.Errorf("keyframes %v: gap %d, want %d", tc.keyframes, got, tc.want)
		}
	}
}

func TestSheetParallelBoundsWorkersAndReportsTheFirstFailure(t *testing.T) {
	var active, peak, ran int
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	err := sheetParallel(context.Background(), 3, 20, func(context.Context, int) error {
		<-gate
		active++
		peak = max(peak, active)
		ran++
		gate <- struct{}{}
		time.Sleep(time.Millisecond)
		<-gate
		active--
		gate <- struct{}{}
		return nil
	})
	if err != nil || ran != 20 || peak > 3 || peak < 2 {
		t.Fatalf("pool: err=%v ran=%d peak=%d", err, ran, peak)
	}
	failure := errors.New("first failure")
	started := make(chan struct{})
	err = sheetParallel(context.Background(), 2, 50, func(ctx context.Context, i int) error {
		if i == 0 {
			// The other worker is already waiting when this task fails.
			<-started
			return failure
		}
		if i == 1 {
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err != failure {
		t.Fatal("first failure lost or replaced by its own cancellation", err)
	}
	if err = sheetParallel(context.Background(), 4, 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSheetImagePathsUseNaturalOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"frame-10.png", "frame-2.PNG", "frame-1.jpg", "frame-02b.jpeg", "notes.txt", "clip.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested.png"), 0700); err != nil {
		t.Fatal(err)
	}
	paths, err := sheetImagePaths([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	if fmt.Sprint(names) != "[frame-1.jpg frame-2.PNG frame-02b.jpeg frame-10.png]" {
		t.Fatalf("order: %v", names)
	}
	paths, err = sheetImagePaths([]string{filepath.Join(dir, "frame-10.png"), filepath.Join(dir, "frame-1.jpg")})
	if err != nil || len(paths) != 2 || filepath.Base(paths[0]) != "frame-1.jpg" {
		t.Fatalf("explicit files: %v %v", paths, err)
	}
	if _, err = sheetImagePaths([]string{filepath.Join(dir, "notes.txt")}); mediaerror.LocalErrorCode(err) != "invalid_argument" {
		t.Fatal("non-image file accepted", err)
	}
	if _, err = sheetImagePaths([]string{t.TempDir()}); mediaerror.LocalErrorCode(err) != "invalid_argument" {
		t.Fatal("empty directory accepted", err)
	}
	if _, err = sheetImagePaths([]string{filepath.Join(dir, "absent.png")}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing image not reported", err)
	}
	for _, pair := range [][2]string{{"a2", "a10"}, {"a02", "a10"}, {"a01", "a1"}, {"a", "a1"}, {"a9b", "a10a"}, {"1", "a"}, {"x1y2", "x1y10"}} {
		if !sheetNaturalLess(pair[0], pair[1]) || sheetNaturalLess(pair[1], pair[0]) {
			t.Errorf("natural order of %q and %q", pair[0], pair[1])
		}
	}
}

func TestSheetImageScalingAveragesAndContains(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 2))
	fillSheetRect(source, 0, 0, 2, 2, 200, 0, 0)
	fillSheetRect(source, 2, 0, 2, 2, 0, 100, 50)
	if tile := scaleSheetImage(source, 2, 1); fmt.Sprint(tile.rgb) != "[200 0 0 0 100 50]" || tile.width != 2 || tile.height != 1 {
		t.Fatalf("halved: %+v", tile)
	}
	if tile := scaleSheetImage(source, 1, 1); fmt.Sprint(tile.rgb) != "[100 50 25]" {
		t.Fatalf("averaged: %+v", tile)
	}
	if tile := scaleSheetImage(source, 8, 4); len(tile.rgb) != 96 || tile.rgb[0] != 200 || tile.rgb[4*3+1] != 100 || tile.rgb[95] != 50 {
		t.Fatalf("enlarged: %+v", tile)
	}
	canvas := newSheetCanvas(12, 8)
	drawSheetTile(canvas, 2, 2, 8, 4, sheetTile{rgb: bytes.Repeat([]byte{9, 8, 7}, 8), width: 4, height: 2})
	at := func(x, y int) string {
		return fmt.Sprint(canvas.Pix[canvas.PixOffset(x, y) : canvas.PixOffset(x, y)+4])
	}
	if at(0, 0) != "[24 24 24 255]" || at(2, 2) != "[0 0 0 255]" || at(4, 3) != "[9 8 7 255]" || at(7, 4) != "[9 8 7 255]" || at(8, 3) != "[0 0 0 255]" || at(4, 5) != "[0 0 0 255]" {
		t.Fatal("tile is not centred on a black cell")
	}
}
