package mediainfo

import (
	"fmt"
	"math"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

// RenderGeometry and RenderFilter retain the established crop/rotation/color
// contract and add an explicit final square-pixel canvas when requested.
func RenderGeometry(stream *Stream, quality string, crop *edit.Crop, canvas *edit.Canvas) (int, int, error) {
	w, h, err := OutputGeometryWithCrop(stream, quality, false, crop)
	if err != nil || canvas == nil {
		return w, h, err
	}
	if err := edit.ValidateRenderOptions(edit.ExportRequest{Format: "mp4", Quality: quality, CutMode: "accurate", Canvas: canvas}); err != nil {
		return 0, 0, &Problem{Code: "invalid_argument", Message: err.Error()}
	}
	return canvas.Width, canvas.Height, nil
}

func RenderFilterWithCanvas(stream *Stream, quality string, crop *edit.Crop, canvas *edit.Canvas) (string, bool, error) {
	filter, hdr, err := CropRenderFilter(stream, quality, crop)
	if err != nil || canvas == nil {
		return filter, hdr, err
	}
	if _, _, err := RenderGeometry(stream, quality, crop, canvas); err != nil {
		return "", false, err
	}
	w, h, _ := OutputGeometryWithCrop(stream, quality, false, crop)
	factor := math.Min(float64(canvas.Width)/float64(w), float64(canvas.Height)/float64(h))
	fw := max(2, min(canvas.Width, int(math.Floor(float64(w)*factor+1e-9))/2*2))
	fh := max(2, min(canvas.Height, int(math.Floor(float64(h)*factor+1e-9))/2*2))
	// Existing color conversion precedes the solid SDR padding. Chroma-aligned
	// placement avoids a one-pixel color shift for subsampled output.
	x, y := (canvas.Width-fw)/4*2, (canvas.Height-fh)/4*2
	filter += fmt.Sprintf(",scale=%d:%d,setsar=1,pad=%d:%d:%d:%d:color=0x%s,setsar=1", fw, fh, canvas.Width, canvas.Height, x, y, canvas.Background[1:])
	return filter, hdr, nil
}

func ValidateRenderedVideoWithCanvas(info Info, source *Stream, quality string, hdr bool, crop *edit.Crop, canvas *edit.Canvas) error {
	if canvas == nil {
		return ValidateRenderedVideoWithCrop(info, source, quality, false, hdr, crop)
	}
	w, h, err := RenderGeometry(source, quality, crop, canvas)
	if err != nil {
		return err
	}
	v := info.SelectedVideo()
	if v == nil || v.Width != w || v.Height != h || v.SampleAspectRatio != "1:1" || math.Abs(v.Rotation()) > .001 {
		return fmt.Errorf("result video does not match the explicit canvas")
	}
	if hdr && (v.ColorPrimaries != "bt709" || v.ColorTransfer != "bt709" || v.ColorSpace != "bt709" || v.ColorRange != "tv" || v.PixelFormat != "yuv420p") {
		return fmt.Errorf("result video does not match the SDR color contract")
	}
	return nil
}
