package mediainfo

import (
	"fmt"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func CropRenderFilter(stream *Stream, quality string, crop *edit.Crop) (string, bool, error) {
	if crop == nil {
		return RenderFilter(stream, quality, false)
	}
	displayWidth, displayHeight, err := DisplayGeometry(stream)
	if err != nil || stream == nil {
		return "", false, ErrUnsupported
	}
	x, y, w, h, err := edit.CropPixelGeometry(displayWidth, displayHeight, crop)
	if err != nil {
		return "", false, &Problem{"crop_invalid", err.Error()}
	}
	width, height, err := OutputGeometryWithCrop(stream, quality, false, crop)
	if err != nil {
		return "", false, err
	}
	// FFmpeg autorotation happens before filters. Normalize SAR before applying
	// the user's rectangle, then apply the normal output resolution cap.
	filter := fmt.Sprintf("scale=%d:%d,setsar=1,crop=%d:%d:%d:%d,scale=%d:%d,setsar=1", displayWidth, displayHeight, w, h, x, y, width, height)
	return ColorRenderFilter(stream, filter)
}
