package app

import (
	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type Crop = edit.Crop

func validateCropMode(request ExportRequest) error {
	return edit.ValidateCropMode(edit.ExportRequest(request))
}

func cropPixelGeometry(width, height int, crop *Crop) (x, y, w, h int, err error) {
	return edit.CropPixelGeometry(width, height, crop)
}

func videoCropRenderFilter(stream *probeStream, quality string, crop *Crop) (string, bool, error) {
	filter, hdr, err := mediainfo.CropRenderFilter(stream, quality, crop)
	return filter, hdr, mediaInfoError(err)
}
