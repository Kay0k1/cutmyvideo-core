package edit

import (
	"errors"
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

func ValidateCropMode(request ExportRequest) error {
	if err := request.Crop.Validate(); err != nil {
		return err
	}
	if request.Crop != nil && (request.Format != "mp4" || request.CutMode != "accurate") {
		return errors.New("crop requires accurate MP4 export; choose accurate MP4 or reset the crop")
	}
	return nil
}

// Each edge rounds to the nearest chroma-aligned pixel boundary, clamped to
// the image. Every boundary differs by at most one displayed pixel.
func CropPixelGeometry(width, height int, crop *Crop) (x, y, w, h int, err error) {
	if err = crop.Validate(); err != nil {
		return
	}
	if crop == nil {
		return 0, 0, width, height, nil
	}
	if crop.Width*float64(width)+1e-9 < 2 || crop.Height*float64(height)+1e-9 < 2 {
		return 0, 0, 0, 0, errors.New("crop is too small for this source; select an area at least two displayed pixels wide and high")
	}
	nearest := func(v float64, size int) int { return min(size/2*2, int(math.Floor(v*float64(size)/2+.5))*2) }
	x, y = nearest(crop.X, width), nearest(crop.Y, height)
	w = nearest(crop.X+crop.Width, width) - x
	h = nearest(crop.Y+crop.Height, height) - y
	if w < 2 || h < 2 || x+w > width || y+h > height {
		return 0, 0, 0, 0, errors.New("crop is too small for this source; select an area at least two displayed pixels wide and high")
	}
	return
}
