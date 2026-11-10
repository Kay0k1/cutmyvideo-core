package localmedia

import (
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
)

const sheetJPEGQuality = 90

// sheetTile is one scaled picture as tightly packed RGB rows.
type sheetTile struct {
	rgb           []byte
	width, height int
}

func newSheetCanvas(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 24, 24, 24, 0xFF
	}
	return img
}

func fillSheetRect(img *image.RGBA, x, y, width, height int, red, green, blue byte) {
	for row := y; row < y+height; row++ {
		at := img.PixOffset(x, row)
		for column := 0; column < width; column++ {
			img.Pix[at], img.Pix[at+1], img.Pix[at+2], img.Pix[at+3] = red, green, blue, 0xFF
			at += 4
		}
	}
}

// drawSheetTile centres a picture in its black tile; bars appear only when the
// picture's proportions differ from the first input's.
func drawSheetTile(img *image.RGBA, x, y, tileW, tileH int, tile sheetTile) {
	fillSheetRect(img, x, y, tileW, tileH, 0, 0, 0)
	left, top := x+(tileW-tile.width)/2, y+(tileH-tile.height)/2
	for row := 0; row < tile.height; row++ {
		source := tile.rgb[row*tile.width*3 : (row+1)*tile.width*3]
		at := img.PixOffset(left, top+row)
		for column := 0; column < tile.width; column++ {
			img.Pix[at], img.Pix[at+1], img.Pix[at+2] = source[column*3], source[column*3+1], source[column*3+2]
			at += 4
		}
	}
}

// drawSheetLabel writes white text on a dark strip directly below a tile. A
// comparison marks pairs under its threshold with a dark red strip.
func drawSheetLabel(img *image.RGBA, l sheetLayout, x, y int, text string, flagged bool) {
	if l.labelH == 0 {
		return
	}
	red := byte(0)
	if flagged {
		red = 0x8B
	}
	fillSheetRect(img, x, y+l.tileH, l.tileW, l.labelH, red, 0, 0)
	drawSheetText(img, x+2*l.scale, y+l.tileH+2*l.scale, l.scale, text, 0xFF, 0xFF, 0xFF)
}

type sheetLimitWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *sheetLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, analysisLimit("an encoded sheet page exceeds its byte limit; lower max_rows, cols or tile_width")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// writeSheetPage encodes one page exclusively. Callers publish it only after
// the whole sheet is complete.
func writeSheetPage(path string, img *image.RGBA, format string, limit int64) (size int64, err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	writer := &sheetLimitWriter{writer: file, remaining: limit}
	if format == "png" {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(writer, img)
	} else {
		err = jpeg.Encode(writer, img, &jpeg.Options{Quality: sheetJPEGQuality})
	}
	if err != nil {
		return 0, err
	}
	return limit - writer.remaining, nil
}
