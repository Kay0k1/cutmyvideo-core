package localmedia

import (
	"context"
	"image"
	"image/draw"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// Decoded images wait for this many source pixels to be free, bounding memory
// when several workers meet large photographs.
const sheetImagePixelBudget = 2 * mediainfo.MaxPixels

func sheetImageProblem() error {
	return &mediaerror.Problem{Code: "unsupported_stream", Message: "Sheet images must be decodable JPEG or PNG files within the picture size limit"}
}

func sheetImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

// sheetImagePaths expands files and directories into one list in natural name
// order, so frame-2 precedes frame-10. Directories are not searched deeper.
func sheetImagePaths(arguments []string) ([]string, error) {
	var paths []string
	for _, argument := range arguments {
		path, err := filepath.Abs(argument)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() || !sheetImageName(path) {
				return nil, analysisArgument("sheet images must be .jpg, .jpeg or .png files or directories of them")
			}
			paths = append(paths, path)
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() && sheetImageName(entry.Name()) {
				paths = append(paths, filepath.Join(path, entry.Name()))
			}
			if len(paths) > SheetMaxCells {
				return nil, analysisLimit("a contact sheet holds at most 2048 cells; choose fewer images")
			}
		}
	}
	if len(paths) == 0 {
		return nil, analysisArgument("no JPEG or PNG images were found for the sheet")
	}
	if len(paths) > SheetMaxCells {
		return nil, analysisLimit("a contact sheet holds at most 2048 cells; choose fewer images")
	}
	sort.SliceStable(paths, func(i, j int) bool { return sheetNaturalLess(paths[i], paths[j]) })
	return paths, nil
}

// sheetNaturalLess orders names with their digit runs compared as numbers.
func sheetNaturalLess(a, b string) bool {
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if digit(a[i]) && digit(b[j]) {
			startA, startB := i, j
			for i < len(a) && digit(a[i]) {
				i++
			}
			for j < len(b) && digit(b[j]) {
				j++
			}
			numberA, numberB := strings.TrimLeft(a[startA:i], "0"), strings.TrimLeft(b[startB:j], "0")
			if len(numberA) != len(numberB) {
				return len(numberA) < len(numberB)
			}
			if numberA != numberB {
				return numberA < numberB
			}
			continue
		}
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	if len(a)-i != len(b)-j {
		return len(a)-i < len(b)-j
	}
	return a < b
}

func sheetImageConfig(file *os.File) (image.Config, error) {
	info, err := file.Stat()
	if err != nil {
		return image.Config{}, err
	}
	if !info.Mode().IsRegular() {
		return image.Config{}, sheetImageProblem()
	}
	config, format, err := image.DecodeConfig(file)
	if err != nil || format != "jpeg" && format != "png" || config.Width <= 0 || config.Height <= 0 || config.Width > mediainfo.MaxPixels/config.Height {
		return image.Config{}, sheetImageProblem()
	}
	return config, nil
}

// sheetImageSize reads only the header: the first image shapes every tile.
func sheetImageSize(path string) (int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	config, err := sheetImageConfig(file)
	return config.Width, config.Height, err
}

// sheetPixelBudget admits decodes while their combined source pixels fit. A
// single image always fits: its own size is bounded before it asks.
type sheetPixelBudget struct {
	mu   sync.Mutex
	free *sync.Cond
	used int
}

func newSheetPixelBudget() *sheetPixelBudget {
	b := &sheetPixelBudget{}
	b.free = sync.NewCond(&b.mu)
	return b
}

func (b *sheetPixelBudget) acquire(pixels int) {
	b.mu.Lock()
	for b.used != 0 && b.used+pixels > sheetImagePixelBudget {
		b.free.Wait()
	}
	b.used += pixels
	b.mu.Unlock()
}

func (b *sheetPixelBudget) release(pixels int) {
	b.mu.Lock()
	b.used -= pixels
	b.mu.Unlock()
	b.free.Broadcast()
}

// loadSheetImage decodes one picture and contains it in a tile. Transparent
// pixels appear over black; embedded orientation tags are not applied.
func loadSheetImage(ctx context.Context, path string, tileW, tileH int, budget *sheetPixelBudget) (sheetTile, error) {
	file, err := os.Open(path)
	if err != nil {
		return sheetTile{}, err
	}
	defer file.Close()
	config, err := sheetImageConfig(file)
	if err != nil {
		return sheetTile{}, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return sheetTile{}, err
	}
	pixels := config.Width * config.Height
	budget.acquire(pixels)
	defer budget.release(pixels)
	if err = ctx.Err(); err != nil {
		return sheetTile{}, err
	}
	decoded, _, err := image.Decode(file)
	if err != nil || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return sheetTile{}, sheetImageProblem()
	}
	source, ok := decoded.(*image.RGBA)
	if !ok {
		source = image.NewRGBA(image.Rect(0, 0, config.Width, config.Height))
		draw.Draw(source, source.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	}
	width, height := sheetFit(config.Width, config.Height, tileW, tileH)
	return scaleSheetImage(source, width, height), nil
}

// scaleSheetImage averages the source pixels each output pixel covers. That
// is an area filter when shrinking and nearest-neighbour when enlarging,
// enough for thumbnails without a resampling dependency.
func scaleSheetImage(source *image.RGBA, width, height int) sheetTile {
	bounds := source.Bounds()
	sourceW, sourceH := int64(bounds.Dx()), int64(bounds.Dy())
	tile := sheetTile{rgb: make([]byte, width*height*3), width: width, height: height}
	for y := 0; y < height; y++ {
		top := int(int64(y) * sourceH / int64(height))
		bottom := max(top+1, int(int64(y+1)*sourceH/int64(height)))
		for x := 0; x < width; x++ {
			left := int(int64(x) * sourceW / int64(width))
			right := max(left+1, int(int64(x+1)*sourceW/int64(width)))
			var red, green, blue int
			for row := top; row < bottom; row++ {
				at := source.PixOffset(bounds.Min.X+left, bounds.Min.Y+row)
				for column := left; column < right; column++ {
					red += int(source.Pix[at])
					green += int(source.Pix[at+1])
					blue += int(source.Pix[at+2])
					at += 4
				}
			}
			count := (right - left) * (bottom - top)
			out := (y*width + x) * 3
			tile.rgb[out], tile.rgb[out+1], tile.rgb[out+2] = byte((red+count/2)/count), byte((green+count/2)/count), byte((blue+count/2)/count)
		}
	}
	return tile
}
