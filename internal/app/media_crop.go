package app

import (
	"errors"
	"fmt"
	"math"
)

// Crop is a rectangle in the upright, square-pixel displayed image. Values are
// normalized to [0,1]; omitting Crop preserves the entire image.
type Crop struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (crop *Crop) Validate() error {
	if crop == nil {
		return nil
	}
	for _, v := range []float64{crop.X, crop.Y, crop.Width, crop.Height} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("crop coordinates must be finite numbers")
		}
	}
	if crop.X < 0 || crop.Y < 0 || crop.Width <= 0 || crop.Height <= 0 || crop.X+crop.Width > 1 || crop.Y+crop.Height > 1 {
		return errors.New("crop must be a nonempty rectangle inside the displayed image (normalized coordinates 0..1)")
	}
	return nil
}

func validateCropMode(request ExportRequest) error {
	if err := request.Crop.Validate(); err != nil {
		return err
	}
	if request.Crop != nil && (request.Format != "mp4" || request.CutMode != "accurate") {
		return errors.New("crop requires accurate MP4 export; choose accurate MP4 or reset the crop")
	}
	return nil
}

// Pixel coordinates are rounded down to a chroma-aligned grid. At most one
// pixel per boundary is lost; there is no stretching or out-of-frame padding.
func cropPixelGeometry(width, height int, crop *Crop) (x, y, w, h int, err error) {
	if err = crop.Validate(); err != nil {
		return
	}
	if crop == nil {
		return 0, 0, width, height, nil
	}
	x = int(math.Floor(crop.X*float64(width)+1e-9)) / 2 * 2
	y = int(math.Floor(crop.Y*float64(height)+1e-9)) / 2 * 2
	w = int(math.Floor(crop.Width*float64(width)+1e-9)) / 2 * 2
	h = int(math.Floor(crop.Height*float64(height)+1e-9)) / 2 * 2
	if w < 2 || h < 2 || x+w > width || y+h > height {
		return 0, 0, 0, 0, errors.New("crop is too small for this source; select an area at least two displayed pixels wide and high")
	}
	return
}

func videoCropRenderFilter(stream *probeStream, quality string, crop *Crop) (string, bool, error) {
	if crop == nil {
		return videoRenderFilter(stream, quality, false)
	}
	displayWidth, displayHeight, err := videoDisplayGeometry(stream)
	if err != nil || stream == nil {
		return "", false, errUnsupportedStream
	}
	x, y, w, h, err := cropPixelGeometry(displayWidth, displayHeight, crop)
	if err != nil {
		return "", false, &sourceProblem{"crop_invalid", err.Error()}
	}
	width, height, err := videoOutputGeometryWithCrop(stream, quality, false, crop)
	if err != nil {
		return "", false, err
	}
	// FFmpeg autorotation happens before filters. Normalize SAR before applying
	// the user's rectangle, then apply the normal output resolution cap.
	filter := fmt.Sprintf("scale=%d:%d,setsar=1,crop=%d:%d:%d:%d,scale=%d:%d,setsar=1", displayWidth, displayHeight, w, h, x, y, width, height)
	return videoColorRenderFilter(stream, filter)
}
