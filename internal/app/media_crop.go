package app

import (
	"fmt"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

type Crop = edit.Crop

func validateCropMode(request ExportRequest) error {
	return edit.ValidateCropMode(edit.ExportRequest(request))
}

func cropPixelGeometry(width, height int, crop *Crop) (x, y, w, h int, err error) {
	return edit.CropPixelGeometry(width, height, crop)
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
