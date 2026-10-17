package app

import (
	"errors"
	"fmt"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func videoDisplayGeometry(stream *probeStream) (int, int, error) {
	width, height, err := mediainfo.DisplayGeometry(stream)
	return width, height, mediaInfoError(err)
}

func videoOutputGeometry(stream *probeStream, quality string, preview bool) (int, int, error) {
	width, height, err := mediainfo.OutputGeometry(stream, quality, preview)
	return width, height, mediaInfoError(err)
}

func videoOutputGeometryWithCrop(stream *probeStream, quality string, preview bool, crop *Crop) (int, int, error) {
	width, height, err := mediainfo.OutputGeometryWithCrop(stream, quality, preview, crop)
	return width, height, mediaInfoError(err)
}

func videoEncoderTimeBase(stream *probeStream) (string, error) {
	timeBase, err := mediainfo.EncoderTimeBase(stream)
	return timeBase, mediaInfoError(err)
}

func videoRenderFilter(stream *probeStream, quality string, preview bool) (string, bool, error) {
	filter, hdr, err := mediainfo.RenderFilter(stream, quality, preview)
	return filter, hdr, mediaInfoError(err)
}

func videoColorRenderFilter(stream *probeStream, filter string) (string, bool, error) {
	result, hdr, err := mediainfo.ColorRenderFilter(stream, filter)
	return result, hdr, mediaInfoError(err)
}

func hdrOutputColorArgs(hdr bool) []string {
	return mediainfo.HDROutputColorArgs(hdr)
}

// Only a classified conversion-capability failure changes the public code.
// Cancellation, storage limits and unrelated processing errors keep their cause.
func renderedMediaFailure(err error, hdr bool) error {
	var process *mediaProcessFailure
	if hdr && errors.As(err, &process) && process.category == "hdr_conversion_unavailable" {
		return fmt.Errorf("%w: %w", &sourceProblem{"hdr_conversion_unavailable", "HDR conversion to SDR is unavailable with the configured media tools"}, err)
	}
	return err
}

func validateRenderedVideo(info probeInfo, source *probeStream, quality string, preview, hdr bool) error {
	return mediaInfoError(mediainfo.ValidateRenderedVideo(mediainfo.Info(info), source, quality, preview, hdr))
}

func validateRenderedVideoWithCrop(info probeInfo, source *probeStream, quality string, preview, hdr bool, crop *Crop) error {
	return mediaInfoError(mediainfo.ValidateRenderedVideoWithCrop(mediainfo.Info(info), source, quality, preview, hdr, crop))
}
