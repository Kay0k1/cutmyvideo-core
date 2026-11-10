package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

// Every frame of the coded source carries its own number: twelve vertical
// bars, bar b white when bit b of the frame number is set. A sheet cell is
// correct only if the bars read back the frame that is on screen at its time.
const sheetCodedPicture = "color=black:size=256x144:rate=25,format=gray,geq=lum='255*mod(floor(N/pow(2,floor(X*12/W))),2)'"

const sheetCodedFrames = 200

func sheetCodedSource(t *testing.T, name string, extra ...string) string {
	t.Helper()
	return sheetCodedSourceAt(t, name, sheetCodedPicture, extra...)
}

func sheetCodedSourceAt(t *testing.T, name, picture string, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	args := []string{"-f", "lavfi", "-t", fmt.Sprint(sheetCodedFrames / 25), "-i", picture}
	args = append(args, extra...)
	analysisFixtureCommand(t, append(args, "-c:v", "libx264", "-g", "50", "-bf", "2", "-pix_fmt", "yuv420p", "-threads", "1", path)...)
	return path
}

func sheetRemux(t *testing.T, input, name string, before []string, after ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	args := append(append([]string{}, before...), "-i", input, "-map", "0", "-c", "copy")
	analysisFixtureCommand(t, append(append(args, after...), path)...)
	return path
}

func sheetImage(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img, format, err := image.Decode(file)
	if err != nil || format != "jpeg" && format != "png" {
		t.Fatalf("independent decode of %s: %s %v", path, format, err)
	}
	return img
}

func sheetPixel(img image.Image, x, y int) (int, int, int) {
	red, green, blue, _ := img.At(x, y).RGBA()
	return int(red >> 8), int(green >> 8), int(blue >> 8)
}

// sheetCellCode reads the frame number from a cell. Bars run along the
// picture's width, or along its height once a rotation is applied.
func sheetCellCode(img image.Image, cell SheetCell, rotated bool) int {
	code := 0
	for bit := 0; bit < 12; bit++ {
		x, y := cell.X+(2*bit+1)*cell.Width/24, cell.Y+cell.Height/2
		if rotated {
			x, y = cell.X+cell.Width/2, cell.Y+(2*bit+1)*cell.Height/24
		}
		if red, green, blue := sheetPixel(img, x, y); red+green+blue > 3*128 {
			code |= 1 << bit
		}
	}
	return code
}

type sheetExpectation struct {
	name     string
	path     string
	expected func(ms int64) int // frame number on screen at a source time
	frameMS  func(frame int) int64
}

func sheetCheckFrames(t *testing.T, e *Engine, source sheetExpectation, options SheetOptions, want []int64) {
	t.Helper()
	options.Inputs = []string{source.path}
	if options.TileWidth == 0 {
		options.TileWidth = 128
	}
	output := filepath.Join(t.TempDir(), "sheet.png")
	result, err := e.GenerateSheet(context.Background(), output, options)
	if err != nil {
		t.Fatalf("%s %+v: %v", source.name, options, err)
	}
	if len(result.Pages) != 1 || result.Pages[0].Path != output || len(result.Cells) != len(want) || result.TileHeight != 72 || result.Mode != "frames" {
		t.Fatalf("%s: pages=%+v cells=%d tile height=%d, want %d cells", source.name, result.Pages, len(result.Cells), result.TileHeight, len(want))
	}
	img := sheetImage(t, output)
	for i, cell := range result.Cells {
		if cell.TimeMS == nil || cell.FrameTimeMS == nil || *cell.TimeMS != want[i] || cell.Index != i+1 || cell.Input != 1 || cell.Path != source.path {
			t.Fatalf("%s: cell %d does not identify its request: %+v, want time %d", source.name, i, cell, want[i])
		}
		frame := source.expected(want[i])
		if got := sheetCellCode(img, cell, false); got != frame || *cell.FrameTimeMS != source.frameMS(frame) {
			t.Errorf("%s at %d ms: cell shows frame %d reported at %d ms, want frame %d at %d ms", source.name, want[i], got, *cell.FrameTimeMS, frame, source.frameMS(frame))
		}
	}
}

func TestSheetShowsTheFrameOnScreenAtEveryRequestedTime(t *testing.T) {
	analysisTools(t)
	constant := func(ms int64) int { return int(min(ms/40, sheetCodedFrames-1)) }
	grid := func(frame int) int64 { return int64(frame) * 40 }
	steady := sheetCodedSource(t, "steady.mp4")
	// Irregular spacing and a two-second hold: frames 0, 1 and 4 of every seven
	// survive, and none between 101 and 149.
	kept := func(frame int) bool {
		return (frame%7 == 0 || frame%7 == 1 || frame%7 == 4) && (frame < 101 || frame > 149)
	}
	variable := sheetCodedSource(t, "variable.mp4", "-vf", "select='(eq(mod(n,7),0)+eq(mod(n,7),1)+eq(mod(n,7),4))*not(between(n,101,149))'", "-fps_mode", "vfr")
	sources := []sheetExpectation{
		{"constant rate", steady, constant, grid},
		// Both containers keep a nonzero start: source time zero is that start.
		{"transport stream", sheetRemux(t, steady, "steady.ts", nil, "-f", "mpegts"), constant, grid},
		{"offset timestamps", sheetRemux(t, steady, "offset.mkv", nil, "-output_ts_offset", "7.3"), constant, grid},
		{"coarse time base", sheetRemux(t, steady, "coarse.mp4", nil, "-video_track_timescale", "25"), constant, grid},
		// 29.97 frames per second: frame n begins at n*1001/30 ms, between
		// whole milliseconds.
		{"fractional rate", sheetCodedSourceAt(t, "ntsc.mp4", strings.Replace(sheetCodedPicture, "rate=25", "rate=30000/1001", 1)),
			func(ms int64) int { return int(min(ms*30/1001, 239)) }, func(frame int) int64 { return (int64(frame)*1001 + 29) / 30 }},
		{"variable rate", variable, func(ms int64) int {
			frame := int(min(ms/40, sheetCodedFrames-1))
			for !kept(frame) {
				frame--
			}
			return frame
		}, grid},
	}
	e := New(Config{})
	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			// Matroska reports its last timestamp as the duration, so the
			// shifted recording also covers times after its final frame.
			info, err := e.Inspect(context.Background(), source.path)
			if err != nil || info.DurationMS < 7800 || info.DurationMS > 8100 && source.name != "offset timestamps" {
				t.Fatalf("fixture duration: %+v %v", info, err)
			}
			// Frame boundaries, both sides of the long hold, repeats and
			// descending order, and the last millisecond of the recording.
			at := []int64{0, 39, 40, 41, 79, 80, 1000, 1001, 4040, 5000, 5990, 6000, 6040, 3999, 4000, 1000, info.DurationMS - 1}
			sheetCheckFrames(t, e, source, SheetOptions{AtMS: at, Threads: 3}, at)
			var every []int64
			for ms := int64(0); ms < info.DurationMS; ms += 700 {
				every = append(every, ms)
			}
			sheetCheckFrames(t, e, source, SheetOptions{EveryMS: 700, Threads: 2}, every)
			var count []int64
			for i := int64(0); i < 9; i++ {
				count = append(count, (2*i+1)*info.DurationMS/18)
			}
			sheetCheckFrames(t, e, source, SheetOptions{Count: 9, Threads: 1}, count)
			// Two close times share one decoder pass. Frames between their
			// seek windows are not delivered, yet the second cell must still
			// show the newest frame before it.
			sheetCheckFrames(t, e, source, SheetOptions{AtMS: []int64{3900, 4250}, Threads: 2}, []int64{3900, 4250})
			// A dense interval decodes each stretch once, not once per frame.
			var dense []int64
			for ms := int64(2000); ms < 5200; ms += 100 {
				dense = append(dense, ms)
			}
			sheetCheckFrames(t, e, source, SheetOptions{StartMS: 2000, EndMS: 5200, EveryMS: 100, Threads: 4}, dense)
		})
	}
	// Picture that begins a second after the container does: earlier times
	// show the first frame and report when it actually appears.
	delayed := filepath.Join(t.TempDir(), "delayed.mkv")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-itsoffset", "1", "-i", steady,
		"-map", "1:v", "-map", "0:a", "-c:v", "copy", "-c:a", "pcm_s16le", "-t", "4", delayed)
	late := sheetExpectation{"delayed picture", delayed, func(ms int64) int { return int(max(0, ms-1000) / 40) }, func(frame int) int64 { return 1000 + int64(frame)*40 }}
	at := []int64{0, 500, 999, 1000, 1039, 1040, 2500, 3999}
	sheetCheckFrames(t, e, late, SheetOptions{AtMS: at}, at)

	// The reported frame time selects the same frame again.
	output := filepath.Join(t.TempDir(), "first.png")
	first, err := e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{variable}, AtMS: []int64{5000, 999}})
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.GenerateSheet(context.Background(), filepath.Join(t.TempDir(), "again.png"), SheetOptions{Inputs: []string{variable}, AtMS: []int64{*first.Cells[0].FrameTimeMS, *first.Cells[1].FrameTimeMS}})
	if err != nil || *again.Cells[0].FrameTimeMS != *first.Cells[0].FrameTimeMS || *again.Cells[1].FrameTimeMS != *first.Cells[1].FrameTimeMS || *first.Cells[0].FrameTimeMS != 3960 || *first.Cells[1].FrameTimeMS != 880 {
		t.Fatalf("frame time is not a stable request: %+v %+v %v", first.Cells, again.Cells, err)
	}
	// Also when the frame begins between two whole milliseconds: frame 30 of
	// the 29.97 fps recording begins at 1001 ms, frame 31 at 1034.37 ms.
	fractional := sources[4]
	sheetCheckFrames(t, e, fractional, SheetOptions{AtMS: []int64{1000, 1001, 1034, 1035}}, []int64{1000, 1001, 1034, 1035})
	if fractional.frameMS(29) != 968 || fractional.frameMS(30) != 1001 || fractional.frameMS(31) != 1035 || fractional.expected(1034) != 30 || fractional.expected(1035) != 31 {
		t.Fatal("fractional-rate oracle")
	}
}

// The cost of a sheet follows its frames, not the recording's length: frames
// near the start of a long recording must not make the decoder run to its end.
func TestSheetDecodesOnlyAroundRequestedTimes(t *testing.T) {
	analysisTools(t)
	dir := t.TempDir()
	clip, long := filepath.Join(dir, "clip.mp4"), filepath.Join(dir, "long.mp4")
	// Ten seconds repeated without re-encoding into 100 minutes of 720p: far
	// more than one decoder thread can decode within the deadline below.
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "color=gray:size=1280x720:rate=50:duration=10", "-c:v", "libx264", "-preset", "ultrafast", "-g", "100", "-threads", "2", clip)
	analysisFixtureCommand(t, "-stream_loop", "599", "-i", clip, "-c", "copy", long)
	e := New(Config{ExportTimeout: 12 * time.Second})
	info, err := e.Inspect(context.Background(), long)
	if err != nil || info.DurationMS < 5_990_000 {
		t.Fatalf("long fixture: %+v %v", info, err)
	}
	started := time.Now()
	result, err := e.GenerateSheet(context.Background(), filepath.Join(dir, "sheet.jpg"), SheetOptions{Inputs: []string{long}, AtMS: []int64{1000, 1500, 9000, 3_000_000}, Threads: 1, TileWidth: 96})
	if err != nil || len(result.Cells) != 4 || *result.Cells[3].FrameTimeMS != 3_000_000 {
		t.Fatalf("frames of a long recording after %v: %+v %v", time.Since(started), result.Cells, err)
	}
}

func TestSheetFollowsDisplayedGeometry(t *testing.T) {
	analysisTools(t)
	e := New(Config{})
	constant := func(ms int64) int { return int(ms / 40) }
	// Anamorphic: 256x144 coded pixels are shown twice as wide.
	wide := sheetCodedSource(t, "anamorphic.mp4", "-vf", "setsar=2")
	output := filepath.Join(t.TempDir(), "wide.png")
	result, err := e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{wide}, AtMS: []int64{0, 1000, 4440}, TileWidth: 256})
	if err != nil || result.TileWidth != 256 || result.TileHeight != 72 {
		t.Fatalf("anamorphic tile: %+v %v", result, err)
	}
	img := sheetImage(t, output)
	for _, cell := range result.Cells {
		if cell.Width != 256 || cell.Height != 72 || sheetCellCode(img, cell, false) != constant(*cell.TimeMS) {
			t.Fatalf("anamorphic cell %+v shows frame %d", cell, sheetCellCode(img, cell, false))
		}
	}
	// A quarter turn: the 256x144 picture is shown upright as 144x256.
	steady := sheetCodedSource(t, "steady.mp4")
	rotated := filepath.Join(t.TempDir(), "rotated.mp4")
	if data, err := sheetFixtureOutput(t, "-display_rotation", "90", "-i", steady, "-map", "0", "-c", "copy", rotated); err != nil {
		if !strings.Contains(string(data), "Unrecognized option 'display_rotation'") {
			t.Fatalf("rotation fixture: %v %s", err, data)
		}
		analysisFixtureCommand(t, "-i", steady, "-map", "0", "-c", "copy", "-metadata:s:v:0", "rotate=90", rotated)
	}
	info, err := e.Inspect(context.Background(), rotated)
	if err != nil || info.DisplayWidth != 144 || info.DisplayHeight != 256 {
		t.Fatalf("rotation fixture is not rotated: %+v %v", info, err)
	}
	output = filepath.Join(t.TempDir(), "rotated.png")
	result, err = e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{rotated}, AtMS: []int64{0, 1000, 4440}, TileWidth: 144})
	if err != nil || result.TileWidth != 144 || result.TileHeight != 256 {
		t.Fatalf("rotated tile: %+v %v", result, err)
	}
	img = sheetImage(t, output)
	for _, cell := range result.Cells {
		// The turn's direction decides whether bar 0 ends at the top or bottom.
		down, up, want := sheetCellCode(img, cell, true), 0, constant(*cell.TimeMS)
		for bit := 0; bit < 12; bit++ {
			up |= (down >> bit & 1) << (11 - bit)
		}
		if cell.Width != 144 || cell.Height != 256 || down != want && up != want || sheetCellCode(img, cell, false) == want && want != 0 {
			t.Fatalf("rotated cell %+v reads %d or %d, want frame %d", cell, down, up, want)
		}
	}
	// A second input with other proportions is contained, not stretched.
	output = filepath.Join(t.TempDir(), "mixed.png")
	result, err = e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{steady, rotated}, AtMS: []int64{1000}, TileWidth: 128, Labels: []string{"index", "name"}})
	if err != nil || len(result.Cells) != 2 || result.TileHeight != 72 || result.Cells[1].Input != 2 || result.Cells[1].Path != rotated || result.Cells[0].Label != "#1 steady.mp4" || result.Cells[1].Label != "#2 rotated.mp4" {
		t.Fatalf("mixed inputs: %+v %v", result, err)
	}
	img = sheetImage(t, output)
	second := result.Cells[1]
	if sheetCellCode(img, result.Cells[0], false) != 25 || second.Width != 128 || second.Height != 72 {
		t.Fatalf("mixed cells: %+v", result.Cells)
	}
	// 144x256 inside 128x72 is about 40 pixels wide: black bars at both sides.
	for _, x := range []int{second.X + 4, second.X + 30, second.X + 98, second.X + 123} {
		if red, green, blue := sheetPixel(img, x, second.Y+second.Height/2); red+green+blue > 30 {
			t.Fatalf("contained picture is stretched at x=%d: %d %d %d", x, red, green, blue)
		}
	}
	lit := 0
	for y := second.Y; y < second.Y+second.Height; y++ {
		if red, _, _ := sheetPixel(img, second.X+second.Width/2, y); red > 128 {
			lit++
		}
	}
	if lit < 10 || lit > 62 {
		t.Fatalf("contained picture is missing from its tile: %d lit rows", lit)
	}
}

func sheetFixtureOutput(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...)...).CombinedOutput()
}

func TestSheetIndexPagesAndExclusiveOutputs(t *testing.T) {
	analysisTools(t)
	input := filepath.Join(t.TempDir(), "source.mp4")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25:duration=4", "-c:v", "libx264", "-threads", "1", input)
	e := New(Config{})
	dir := t.TempDir()
	output := filepath.Join(dir, "sheet.jpg")
	result, err := e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{input}, Count: 20, Columns: 4, MaxRows: 2, TileWidth: 96, Labels: []string{"index", "time"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || result.Format != "jpeg" || result.Columns != 4 || result.TileWidth != 96 || result.TileHeight != 54 || result.LabelHeight <= 0 || fmt.Sprint(result.Labels) != "[index time]" || len(result.Pages) != 3 || len(result.Cells) != 20 {
		t.Fatalf("index header: %+v", result)
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("a paged sheet also wrote its unnumbered name", err)
	}
	perPage := []int{8, 8, 4}
	var rectangles [][]image.Rectangle
	for i, page := range result.Pages {
		if page.Page != i+1 || page.Path != filepath.Join(dir, fmt.Sprintf("sheet-%03d.jpg", i+1)) {
			t.Fatalf("page %d name: %+v", i+1, page)
		}
		stat, err := os.Stat(page.Path)
		if err != nil || stat.Size() != page.SizeBytes || page.SizeBytes <= 0 {
			t.Fatalf("page %d size: %+v %v", i+1, page, err)
		}
		bounds := sheetImage(t, page.Path).Bounds()
		if bounds.Dx() != page.Width || bounds.Dy() != page.Height || page.Width != result.Pages[0].Width || i < 2 && page.Height != result.Pages[0].Height || i == 2 && page.Height >= result.Pages[0].Height {
			t.Fatalf("page %d geometry: %+v decoded %v", i+1, page, bounds)
		}
		rectangles = append(rectangles, nil)
	}
	for i, cell := range result.Cells {
		page := result.Pages[cell.Page-1]
		rectangle := image.Rect(cell.X, cell.Y, cell.X+cell.Width, cell.Y+cell.Height+result.LabelHeight)
		if cell.Index != i+1 || cell.Page != i/8+1 || cell.Column != i%8%4 || cell.Row != i%8/4 || cell.Width != 96 || cell.Height != 54 || !rectangle.In(image.Rect(0, 0, page.Width, page.Height)) {
			t.Fatalf("cell %d position: %+v on %+v", i+1, cell, page)
		}
		for _, other := range rectangles[cell.Page-1] {
			if rectangle.Overlaps(other) {
				t.Fatalf("cell %d overlaps %v", i+1, other)
			}
		}
		rectangles[cell.Page-1] = append(rectangles[cell.Page-1], rectangle)
		if want := fmt.Sprintf("#%d %s", i+1, fmt.Sprintf("00:%02d.%03d", *cell.TimeMS/1000, *cell.TimeMS%1000)); cell.Label != want || *cell.TimeMS != (2*int64(i)+1)*4000/40 {
			t.Fatalf("cell %d label %q time %d, want %q", i+1, cell.Label, *cell.TimeMS, want)
		}
	}
	for page, list := range rectangles {
		if len(list) != perPage[page] {
			t.Fatalf("page %d holds %d cells", page+1, len(list))
		}
	}
	// Captions are drawn in white on the dark strip below each tile.
	first := sheetImage(t, result.Pages[0].Path)
	cell := result.Cells[0]
	bright, dark := 0, 0
	for y := cell.Y + cell.Height; y < cell.Y+cell.Height+result.LabelHeight; y++ {
		for x := cell.X; x < cell.X+cell.Width; x++ {
			red, green, blue := sheetPixel(first, x, y)
			if red > 180 && green > 180 && blue > 180 {
				bright++
			} else if red < 70 && green < 70 && blue < 70 {
				dark++
			}
		}
	}
	if bright < 30 || dark < 3*bright {
		t.Fatalf("caption strip: %d bright and %d dark pixels", bright, dark)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &index); err != nil || string(index["version"]) != "1" || string(index["pairs"]) != "[]" || string(index["below_threshold"]) != "[]" || string(index["label"]) != `["index","time"]` || index["ssim_threshold"] != nil {
		t.Fatalf("index JSON: %s %v", encoded, err)
	}
	var cells []map[string]json.RawMessage
	if err = json.Unmarshal(index["cells"], &cells); err != nil || len(cells) != 20 {
		t.Fatal(err)
	}
	for _, field := range []string{"index", "page", "col", "row", "x", "y", "width", "height", "input", "path", "time_ms", "frame_time_ms", "label"} {
		if cells[0][field] == nil {
			t.Fatalf("cell JSON lacks %s: %s", field, index["cells"])
		}
	}

	// Existing pages are never replaced, whichever page is occupied.
	before := map[string][]byte{}
	for _, page := range result.Pages {
		before[page.Path], _ = os.ReadFile(page.Path)
	}
	unchanged := func() {
		t.Helper()
		for path, data := range before {
			if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, data) {
				t.Fatalf("existing page %s was modified: %v", path, err)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != len(before) {
			t.Fatalf("refused sheet left files behind: %v %v", entries, err)
		}
	}
	if _, err = e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{input}, Count: 20, Columns: 4, MaxRows: 2, TileWidth: 96}); CodeOf(err) != CodeOutputExists || !errors.Is(err, ErrOutputExists) {
		t.Fatal("occupied page names were accepted", err)
	}
	unchanged()
	if err = os.Remove(result.Pages[0].Path); err != nil {
		t.Fatal(err)
	}
	delete(before, result.Pages[0].Path)
	if _, err = e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{input}, Count: 20, Columns: 4, MaxRows: 2, TileWidth: 96}); CodeOf(err) != CodeOutputExists {
		t.Fatal("a later occupied page was overwritten or ignored", err)
	}
	unchanged()

	// A single page is exactly the requested name; fewer frames than columns
	// narrow the page instead of leaving empty cells.
	single := filepath.Join(t.TempDir(), "single.PNG")
	result, err = e.GenerateSheet(context.Background(), single, SheetOptions{Inputs: []string{input}, Count: 3, Labels: []string{"none"}})
	if err != nil || len(result.Pages) != 1 || result.Pages[0].Path != single || result.Format != "png" || result.Columns != 3 || result.LabelHeight != 0 || result.Labels == nil || len(result.Labels) != 0 || result.Cells[2].Label != "" {
		t.Fatalf("single page: %+v %v", result, err)
	}
	if bounds := sheetImage(t, single).Bounds(); bounds.Dx() != result.Pages[0].Width || bounds.Dy() != result.Pages[0].Height || bounds.Dx() > 3*330 {
		t.Fatalf("single page geometry: %v", bounds)
	}
	data, _ := os.ReadFile(single)
	for _, options := range []SheetOptions{{Inputs: []string{input}, Count: 3}, {Inputs: []string{input}, Count: 200, MaxRows: 1}} {
		if _, err = e.GenerateSheet(context.Background(), single, options); CodeOf(err) != CodeOutputExists {
			t.Fatal("existing output accepted", err)
		}
	}
	if now, _ := os.ReadFile(single); !bytes.Equal(now, data) {
		t.Fatal("existing sheet bytes changed")
	}
	link := filepath.Join(t.TempDir(), "dangling.jpg")
	if err = os.Symlink(filepath.Join(t.TempDir(), "absent.jpg"), link); err == nil {
		if _, err = e.GenerateSheet(context.Background(), link, SheetOptions{Inputs: []string{input}, Count: 3}); CodeOf(err) != CodeOutputExists {
			t.Fatal("dangling symlink was followed", err)
		}
	}
}

func TestSheetCompareMeasuresEachPair(t *testing.T) {
	analysisTools(t)
	dir := t.TempDir()
	original, altered := filepath.Join(dir, "original.mp4"), filepath.Join(dir, "altered.mp4")
	picture := "testsrc2=size=192x108:rate=25:duration=6"
	analysisFixtureCommand(t, "-f", "lavfi", "-i", picture, "-c:v", "libx264", "-qp", "18", "-threads", "1", original)
	// Two seconds of the same recording are replaced by other material.
	analysisFixtureCommand(t, "-f", "lavfi", "-i", picture+",negate=enable='between(t,2,3.99)',hflip=enable='between(t,2,3.99)'", "-c:v", "libx264", "-qp", "18", "-threads", "1", altered)
	e := New(Config{})
	output := filepath.Join(dir, "same.png")
	same, err := e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{original, original}, Compare: true, EveryMS: 500, TileWidth: 192})
	if err != nil || same.Mode != "compare" || len(same.Pairs) != 12 || len(same.Cells) != 24 || same.BelowThreshold == nil || len(same.BelowThreshold) != 0 || same.SSIMThreshold != 0.95 {
		t.Fatalf("identical recordings: %+v %v", same, err)
	}
	for _, pair := range same.Pairs {
		if pair.SSIM != 1 {
			t.Fatalf("identical pair %+v", pair)
		}
	}
	output = filepath.Join(dir, "changed.png")
	changed, err := e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{original, altered}, Compare: true, EveryMS: 500, TileWidth: 192, SSIMThreshold: 0.9})
	if err != nil || len(changed.Pairs) != 12 || len(changed.Cells) != 24 || changed.Columns != 8 || changed.SSIMThreshold != 0.9 {
		t.Fatalf("altered recording: %+v %v", changed, err)
	}
	img := sheetImage(t, output)
	var below []int64
	for i, pair := range changed.Pairs {
		first, second := changed.Cells[2*i], changed.Cells[2*i+1]
		replaced := pair.TimeMS >= 2000 && pair.TimeMS < 4000
		if pair.Index != i+1 || pair.TimeMS != int64(i)*500 || pair.Cells != [2]int{2*i + 1, 2*i + 2} || first.Input != 1 || second.Input != 2 || first.Path != original || second.Path != altered ||
			*first.TimeMS != pair.TimeMS || *second.TimeMS != pair.TimeMS || first.Row != second.Row || second.Column != first.Column+1 || first.Page != second.Page {
			t.Fatalf("pair %d does not place the same moment side by side: %+v %+v %+v", i+1, pair, first, second)
		}
		if replaced != (pair.SSIM < 0.9) || !replaced && pair.SSIM < 0.97 || replaced && pair.SSIM > 0.6 {
			t.Fatalf("pair at %d ms: ssim %v, replaced=%v", pair.TimeMS, pair.SSIM, replaced)
		}
		if want := fmt.Sprintf("00:%02d.%03d ssim %.3f", pair.TimeMS/1000, pair.TimeMS%1000, pair.SSIM); second.Label != want || strings.Contains(first.Label, "ssim") {
			t.Fatalf("pair labels %q %q, want %q", first.Label, second.Label, want)
		}
		// Pairs under the threshold carry a dark red caption strip.
		for _, cell := range []SheetCell{first, second} {
			red, green, blue := sheetPixel(img, cell.X+cell.Width-3, cell.Y+cell.Height+changed.LabelHeight/2)
			if marked := red > 100 && green < 40 && blue < 40; marked != replaced || !marked && red+green+blue > 40 {
				t.Fatalf("pair at %d ms: caption strip %d %d %d, replaced=%v", pair.TimeMS, red, green, blue, replaced)
			}
		}
		if replaced {
			below = append(below, pair.TimeMS)
		}
	}
	if fmt.Sprint(below) != "[2000 2500 3000 3500]" || len(changed.BelowThreshold) != 4 {
		t.Fatalf("below threshold: %v %+v", below, changed.BelowThreshold)
	}
	for i, pair := range changed.BelowThreshold {
		if pair.TimeMS != below[i] || pair != changed.Pairs[pair.Index-1] {
			t.Fatalf("below-threshold entry %+v", pair)
		}
	}
	// The shorter recording bounds the compared interval.
	short := filepath.Join(dir, "short.mp4")
	analysisFixtureCommand(t, "-i", original, "-t", "2", "-c", "copy", short)
	result, err := e.GenerateSheet(context.Background(), filepath.Join(dir, "short.jpg"), SheetOptions{Inputs: []string{original, short}, Compare: true, Count: 4})
	if err != nil || len(result.Pairs) != 4 || result.Pairs[3].TimeMS >= 2000 || result.Columns != 8 {
		t.Fatalf("shorter recording: %+v %v", result, err)
	}
	if _, err = e.GenerateSheet(context.Background(), filepath.Join(dir, "late.jpg"), SheetOptions{Inputs: []string{original, short}, Compare: true, AtMS: []int64{3000}}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("time beyond the shorter recording accepted", err)
	}
}

func TestSheetScenesShowTheMiddleOfEveryScene(t *testing.T) {
	analysisTools(t)
	input := filepath.Join(t.TempDir(), "scenes.mp4")
	analysisFixtureCommand(t,
		"-f", "lavfi", "-i", "color=black:s=160x90:r=25:d=2",
		"-f", "lavfi", "-i", "color=yellow:s=160x90:r=25:d=3",
		"-f", "lavfi", "-i", "color=blue:s=160x90:r=25:d=1",
		"-filter_complex", "[0:v][1:v][2:v]concat=n=3:v=1:a=0[v]", "-map", "[v]", "-c:v", "libx264", "-threads", "1", input)
	e := New(Config{})
	scenes := func(t *testing.T, options SheetOptions, want []int64) {
		t.Helper()
		options.Inputs, options.Scenes, options.TileWidth = []string{input}, true, 96
		output := filepath.Join(t.TempDir(), "scenes.png")
		result, err := e.GenerateSheet(context.Background(), output, options)
		if err != nil || len(result.Cells) != len(want) {
			t.Fatalf("scene sheet: %+v %v", result, err)
		}
		img := sheetImage(t, output)
		for i, cell := range result.Cells {
			red, green, blue := sheetPixel(img, cell.X+cell.Width/2, cell.Y+cell.Height/2)
			var scene string
			switch {
			case red < 40 && green < 40 && blue < 40:
				scene = "black"
			case red > 180 && green > 180 && blue < 80:
				scene = "yellow"
			case red < 80 && green < 80 && blue > 180:
				scene = "blue"
			}
			at := *cell.TimeMS
			expected := map[bool]string{true: "black", false: map[bool]string{true: "yellow", false: "blue"}[at < 5000]}[at < 2000]
			if at != want[i] || scene != expected {
				t.Fatalf("scene cell %d at %d ms shows %q (%d %d %d), want %q at %d ms", i+1, at, scene, red, green, blue, expected, want[i])
			}
		}
	}
	scenes(t, SheetOptions{}, []int64{1000, 3500, 5500})
	scenes(t, SheetOptions{StartMS: 1000, EndMS: 5600}, []int64{1500, 3500, 5300})
	analysis, err := e.AnalyzeVideo(context.Background(), input, VideoAnalysisOptions{})
	if err != nil || len(analysis.Scenes) != 2 {
		t.Fatalf("scene fixture: %+v %v", analysis, err)
	}
	scenes(t, SheetOptions{SceneAnalysis: &analysis}, []int64{1000, 3500, 5500})
	window, err := e.AnalyzeVideo(context.Background(), input, VideoAnalysisOptions{StartMS: 500, EndMS: 3000})
	if err != nil {
		t.Fatal(err)
	}
	scenes(t, SheetOptions{SceneAnalysis: &window}, []int64{1250, 2500})
	for name, bad := range map[string]VideoAnalysisResult{
		"version":   {Version: 2, EndMS: 6000},
		"too long":  {Version: 1, EndMS: 60000},
		"unordered": {Version: 1, EndMS: 6000, Scenes: []SceneChange{{AtMS: 5000}, {AtMS: 2000}}},
		"outside":   {Version: 1, StartMS: 5000, EndMS: 6000},
	} {
		options := SheetOptions{Inputs: []string{input}, Scenes: true, SceneAnalysis: &bad}
		if name == "outside" {
			options.EndMS = 4000
		}
		if _, err := e.GenerateSheet(context.Background(), filepath.Join(t.TempDir(), "bad.jpg"), options); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%s analysis accepted: %v", name, err)
		}
	}
}

func TestSheetFromImagesOfDifferentSizes(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, width, height int, fill color.RGBA) string {
		t.Helper()
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = fill.R, fill.G, fill.B, 0xFF
		}
		var data bytes.Buffer
		var err error
		if strings.HasSuffix(name, ".png") {
			err = png.Encode(&data, img)
		} else {
			err = jpeg.Encode(&data, img, &jpeg.Options{Quality: 95})
		}
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err = os.WriteFile(path, data.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	wide := write("shot-1.png", 320, 180, color.RGBA{R: 220})
	tall := write("shot-2.jpg", 90, 300, color.RGBA{G: 220})
	small := write("shot-10.png", 16, 9, color.RGBA{B: 220})
	large := write("shot-9.jpeg", 1600, 900, color.RGBA{R: 220, G: 220})
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	e := New(Config{})
	output := filepath.Join(t.TempDir(), "images.png")
	result, err := e.GenerateSheet(context.Background(), output, SheetOptions{Images: []string{dir}, TileWidth: 96, Columns: 3, Labels: []string{"index", "name"}})
	if err != nil || result.Mode != "images" || len(result.Cells) != 4 || len(result.Pages) != 1 || result.TileHeight != 54 || result.Columns != 3 {
		t.Fatalf("image sheet: %+v %v", result, err)
	}
	img := sheetImage(t, output)
	for i, want := range []struct {
		path             string
		red, green, blue int
	}{{wide, 220, 0, 0}, {tall, 0, 220, 0}, {large, 220, 220, 0}, {small, 0, 0, 220}} {
		cell := result.Cells[i]
		red, green, blue := sheetPixel(img, cell.X+cell.Width/2, cell.Y+cell.Height/2)
		if cell.Path != want.path || cell.Input != i+1 || cell.Index != i+1 || cell.TimeMS != nil || cell.FrameTimeMS != nil || cell.Label != fmt.Sprintf("#%d %s", i+1, filepath.Base(want.path)) ||
			absInt(red-want.red) > 12 || absInt(green-want.green) > 12 || absInt(blue-want.blue) > 12 {
			t.Fatalf("image cell %d: %+v shows %d %d %d", i+1, cell, red, green, blue)
		}
	}
	// The portrait image keeps its proportions between black bars.
	portrait := result.Cells[1]
	for _, x := range []int{portrait.X + 2, portrait.X + 36, portrait.X + 59, portrait.X + 93} {
		if red, green, blue := sheetPixel(img, x, portrait.Y+portrait.Height/2); red+green+blue > 30 {
			t.Fatalf("portrait image is stretched at x=%d: %d %d %d", x, red, green, blue)
		}
	}
	encoded, _ := json.Marshal(result.Cells[0])
	if strings.Contains(string(encoded), "time_ms") {
		t.Fatalf("image cell reports a time: %s", encoded)
	}
	// Explicit files and several pages.
	output = filepath.Join(t.TempDir(), "pages.jpg")
	result, err = e.GenerateSheet(context.Background(), output, SheetOptions{Images: []string{small, wide, tall}, Columns: 1, MaxRows: 2, TileWidth: 64})
	if err != nil || len(result.Pages) != 2 || len(result.Cells) != 3 || result.Cells[0].Path != wide || result.Cells[2].Path != small || result.Cells[2].Page != 2 || result.Cells[0].Label != "shot-1.png" || filepath.Base(result.Pages[1].Path) != "pages-002.jpg" {
		t.Fatalf("explicit images: %+v %v", result, err)
	}
	broken := filepath.Join(t.TempDir(), "broken.png")
	if err = os.WriteFile(broken, []byte("\x89PNG\r\n\x1a\nnot really"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		options SheetOptions
		code    ErrorCode
	}{
		"broken image":    {SheetOptions{Images: []string{wide, broken}}, CodeUnsupportedMedia},
		"missing image":   {SheetOptions{Images: []string{filepath.Join(dir, "absent.png")}}, CodeInputNotFound},
		"not an image":    {SheetOptions{Images: []string{filepath.Join(dir, "notes.txt")}}, CodeInvalidArgument},
		"empty directory": {SheetOptions{Images: []string{t.TempDir()}}, CodeInvalidArgument},
		"time selection":  {SheetOptions{Images: []string{dir}, Count: 3}, CodeInvalidArgument},
	} {
		target := filepath.Join(t.TempDir(), "refused.jpg")
		if _, err = e.GenerateSheet(context.Background(), target, test.options); CodeOf(err) != test.code {
			t.Errorf("%s: %v, want %s", name, err, test.code)
		}
		if _, err = os.Lstat(target); !os.IsNotExist(err) {
			t.Errorf("%s published a sheet", name)
		}
	}
}

func TestSheetConvertsHDRToSDR(t *testing.T) {
	analysisTools(t)
	e := New(Config{})
	for _, name := range []string{"pq-10bit.mp4", "hlg-10bit.mp4"} {
		input := filepath.Join("..", "..", "internal", "app", "testdata", "media-contract", name)
		output := filepath.Join(t.TempDir(), "hdr.png")
		result, err := e.GenerateSheet(context.Background(), output, SheetOptions{Inputs: []string{input}, AtMS: []int64{1000}, TileWidth: 128, Labels: []string{"none"}})
		if err != nil || result.TileHeight != 80 {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		img, cell := sheetImage(t, output), result.Cells[0]
		// Seven neutral bars stay neutral and ordered after tone mapping.
		last := -1
		levels := make([]int, 7)
		for i := range levels {
			red, green, blue := sheetPixel(img, cell.X+(2*i+1)*128/16, cell.Y+40)
			levels[i] = red
			if absInt(red-green) > 5 || absInt(green-blue) > 5 || i == 0 && red > 8 || i > 0 && red <= last {
				t.Fatalf("%s: HDR signal became tinted or nonmonotonic: %v", name, levels)
			}
			last = red
		}
		if levels[4] < 190 || levels[4] > 250 || levels[6] < 230 {
			t.Fatalf("%s: tone map changed: %v", name, levels)
		}
	}
}

func TestSheetRefusalsLimitsAndDeadlines(t *testing.T) {
	analysisTools(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mp4")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25:duration=3", "-c:v", "libx264", "-threads", "1", input)
	audio := analysisWAV(t, 1, 1000, func(int64) bool { return true })
	e := New(Config{})
	one := []string{input}
	for name, test := range map[string]struct {
		output  string
		options SheetOptions
		code    ErrorCode
	}{
		"no output":         {"", SheetOptions{Inputs: one}, CodeInvalidArgument},
		"unknown suffix":    {"sheet.gif", SheetOptions{Inputs: one}, CodeInvalidArgument},
		"format mismatch":   {"sheet.jpg", SheetOptions{Inputs: one, Format: "png"}, CodeInvalidArgument},
		"no inputs":         {"sheet.jpg", SheetOptions{}, CodeInvalidArgument},
		"two selections":    {"sheet.jpg", SheetOptions{Inputs: one, Count: 4, EveryMS: 1000}, CodeInvalidArgument},
		"audio only":        {"sheet.jpg", SheetOptions{Inputs: []string{audio}}, CodeInvalidArgument},
		"second input":      {"sheet.jpg", SheetOptions{Inputs: []string{input, audio}}, CodeInvalidArgument},
		"time at duration":  {"sheet.jpg", SheetOptions{Inputs: one, AtMS: []int64{3000}}, CodeInvalidArgument},
		"time beyond":       {"sheet.jpg", SheetOptions{Inputs: one, AtMS: []int64{0, 999999}}, CodeInvalidArgument},
		"range beyond":      {"sheet.jpg", SheetOptions{Inputs: one, EndMS: 3001}, CodeInvalidArgument},
		"range start":       {"sheet.jpg", SheetOptions{Inputs: one, StartMS: 3000}, CodeInvalidArgument},
		"time before range": {"sheet.jpg", SheetOptions{Inputs: one, StartMS: 1000, AtMS: []int64{500}}, CodeInvalidArgument},
		"missing input":     {"sheet.jpg", SheetOptions{Inputs: []string{filepath.Join(dir, "absent.mp4")}}, CodeInputNotFound},
		"too many cells":    {"sheet.jpg", SheetOptions{Inputs: one, EveryMS: 1}, CodeOutputLimit},
		"cells of inputs":   {"sheet.jpg", SheetOptions{Inputs: []string{input, input, input}, Count: 700}, CodeOutputLimit},
		"page pixels":       {"sheet.jpg", SheetOptions{Inputs: one, Count: 64, Columns: 64, TileWidth: 1920}, CodeOutputLimit},
		"page count":        {"sheet.jpg", SheetOptions{Inputs: one, Count: 257, Columns: 1, MaxRows: 1}, CodeOutputLimit},
		"missing directory": {filepath.Join("absent", "sheet.jpg"), SheetOptions{Inputs: one}, CodePublicationFailed},
	} {
		target := test.output
		if target != "" {
			target = filepath.Join(t.TempDir(), target)
		}
		result, err := e.GenerateSheet(context.Background(), target, test.options)
		if CodeOf(err) != test.code || len(result.Pages) != 0 {
			t.Errorf("%s: %v (%s), want %s", name, err, CodeOf(err), test.code)
		}
		if target != "" {
			if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 0 {
				t.Errorf("%s left files behind: %v", name, entries)
			}
		}
	}
	if _, err := New(Config{MaxOutputBytes: -1}).GenerateSheet(context.Background(), filepath.Join(dir, "config.jpg"), SheetOptions{Inputs: one}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("invalid engine configuration accepted", err)
	}
	limited := filepath.Join(t.TempDir(), "limited.png")
	if _, err := New(Config{MaxOutputBytes: 64}).GenerateSheet(context.Background(), limited, SheetOptions{Inputs: one, Count: 4}); CodeOf(err) != CodeOutputLimit {
		t.Fatal("encoded byte limit ignored", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(limited)); len(entries) != 0 {
		t.Fatalf("oversized sheet left files behind: %v", entries)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	target := filepath.Join(t.TempDir(), "cancelled.jpg")
	if _, err := e.GenerateSheet(cancelled, target, SheetOptions{Inputs: one}); !errors.Is(err, context.Canceled) || CodeOf(err) != CodeCancelled {
		t.Fatal("cancellation lost", err)
	}
	if _, err := New(Config{ExportTimeout: time.Nanosecond}).GenerateSheet(context.Background(), target, SheetOptions{Inputs: one}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("whole-operation deadline lost", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 0 {
		t.Fatalf("stopped sheet left files behind: %v", entries)
	}

	// The input may not change while its frames are read.
	changing := filepath.Join(t.TempDir(), "changing.mp4")
	data, err := os.ReadFile(input)
	if err != nil || os.WriteFile(changing, data, 0600) != nil {
		t.Fatal(err)
	}
	touched := false
	ctx := localmedia.WithProcessObserver(context.Background(), func(_ context.Context, tool string, _ time.Duration, _ error) {
		if tool == "ffmpeg" && !touched {
			touched = true
			stamp := time.Now().Add(time.Hour)
			if err := os.Chtimes(changing, stamp, stamp); err != nil {
				t.Error(err)
			}
		}
	})
	target = filepath.Join(t.TempDir(), "changed.jpg")
	if _, err = e.GenerateSheet(ctx, target, SheetOptions{Inputs: []string{changing}, Count: 4}); CodeOf(err) != CodeSourceChanged || !touched {
		t.Fatal("source mutation accepted", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 0 {
		t.Fatalf("sheet of a changed source left files behind: %v", entries)
	}

	if runtime.GOOS == "windows" {
		return
	}
	// A decoder that never produces a frame cannot outlive the deadline.
	stalled := filepath.Join(t.TempDir(), "stalled-ffmpeg")
	if err = os.WriteFile(stalled, []byte("#!/bin/sh\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(t.TempDir(), "stalled.jpg")
	started := time.Now()
	_, err = New(Config{FFmpegPath: stalled, ExportTimeout: 700 * time.Millisecond}).GenerateSheet(context.Background(), target, SheetOptions{Inputs: one, Count: 6})
	if CodeOf(err) != CodeTimeout || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 10*time.Second {
		t.Fatal("stalled decoder outlived the deadline", err, time.Since(started))
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 0 {
		t.Fatalf("stalled sheet left files behind: %v", entries)
	}
	// A tool that exits without frames is a failure, never an empty sheet.
	silent := filepath.Join(t.TempDir(), "silent-ffmpeg")
	if err = os.WriteFile(silent, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = New(Config{FFmpegPath: silent}).GenerateSheet(context.Background(), target, SheetOptions{Inputs: one, Count: 2}); CodeOf(err) != CodeUnsupportedMedia {
		t.Fatal("frameless decoder accepted", err)
	}
	if _, err = New(Config{FFmpegPath: filepath.Join(dir, "absent-ffmpeg")}).GenerateSheet(context.Background(), target, SheetOptions{Inputs: one, Count: 2}); CodeOf(err) != CodeToolUnavailable {
		t.Fatal("missing decoder not classified", err)
	}
}
