package app

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Display dimensions describe square pixels after applying the container's
// right-angle display rotation. Coded Width/Height keep their original meaning.
func videoDisplayGeometry(stream *probeStream) (int, int, error) {
	if stream == nil {
		return 0, 0, nil
	}
	sar := 1.0
	if value := stream.SampleAspectRatio; value != "" && value != "N/A" && value != "0:1" {
		parts := strings.Split(value, ":")
		if len(parts) != 2 {
			return 0, 0, errUnsupportedStream
		}
		numerator, err := strconv.ParseInt(parts[0], 10, 32)
		denominator, denomErr := strconv.ParseInt(parts[1], 10, 32)
		if err != nil || denomErr != nil || numerator <= 0 || denominator <= 0 {
			return 0, 0, errUnsupportedStream
		}
		sar = float64(numerator) / float64(denominator)
	}
	width, height := math.Round(float64(stream.Width)*sar), float64(stream.Height)
	rotation := math.Mod(stream.rotation(), 360)
	if rotation < 0 {
		rotation += 360
	}
	quarterTurns := math.Round(rotation / 90)
	if math.Abs(rotation-quarterTurns*90) > 0.001 {
		return 0, 0, errUnsupportedStream
	}
	if int(quarterTurns)%2 != 0 {
		width, height = height, width
	}
	if width < 2 || height < 2 || math.IsNaN(width) || math.IsInf(width, 0) || width > float64(maxMediaPixels)/height {
		return 0, 0, errUnsupportedStream
	}
	return int(width), int(height), nil
}

func videoOutputGeometry(stream *probeStream, quality string, preview bool) (int, int, error) {
	return videoOutputGeometryWithCrop(stream, quality, preview, nil)
}

func videoOutputGeometryWithCrop(stream *probeStream, quality string, preview bool, crop *Crop) (int, int, error) {
	width, height, err := videoDisplayGeometry(stream)
	if err != nil || stream == nil {
		return 0, 0, errUnsupportedStream
	}
	if crop != nil {
		_, _, width, height, err = cropPixelGeometry(width, height, crop)
		if err != nil {
			return 0, 0, &sourceProblem{"crop_invalid", err.Error()}
		}
	}
	factor := 1.0
	if preview {
		factor = min(factor, 854.0/float64(width), 480.0/float64(height))
	} else if quality != "best" {
		capHeight := 1080.0
		if quality == "720p" {
			capHeight = 720
		}
		factor = min(factor, capHeight/float64(height))
	}
	// H.264 4:2:0 needs even dimensions. Apply the same ratio to both axes;
	// merely resetting SAR would stretch anamorphic input.
	return max(2, int(float64(width)*factor)/2*2), max(2, int(float64(height)*factor)/2*2), nil
}

func videoEncoderTimeBase(stream *probeStream) (string, error) {
	parts := strings.Split(stream.TimeBase, "/")
	if len(parts) != 2 {
		return "", errUnsupportedStream
	}
	numerator, err := strconv.ParseInt(parts[0], 10, 32)
	denominator, denominatorErr := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || denominatorErr != nil || numerator <= 0 || denominator <= 0 {
		return "", errUnsupportedStream
	}
	// An explicit positive ratio works across supported FFmpeg versions.
	// Historical "-1" and newer "demux" spellings differ between releases.
	return fmt.Sprintf("%d/%d", numerator, denominator), nil
}

func videoRenderFilter(stream *probeStream, quality string, preview bool) (string, bool, error) {
	width, height, err := videoOutputGeometry(stream, quality, preview)
	if err != nil {
		return "", false, err
	}
	filter := fmt.Sprintf("scale=%d:%d,setsar=1", width, height)
	if preview {
		// Discard frames before scaling and HDR conversion. High frame-rate
		// recordings otherwise pay for processing frames the preview drops.
		// fps uses the same source timestamps and 24 fps policy as before.
		filter = "fps=24," + filter
	}
	return videoColorRenderFilter(stream, filter)
}

func videoColorRenderFilter(stream *probeStream, filter string) (string, bool, error) {
	hdr := stream.ColorTransfer == "smpte2084" || stream.ColorTransfer == "arib-std-b67"
	if hdr {
		// PQ/HLG must be linearized before tone mapping. npl sets the 100-nit
		// SDR reference white; use the PQ legal peak or HLG reference peak.
		// Missing/contradictory colorimetry is not replaced by guessed tags.
		if stream.ColorPrimaries != "bt2020" && stream.ColorPrimaries != "bt709" ||
			stream.ColorSpace != "bt2020nc" && stream.ColorSpace != "bt2020c" && stream.ColorSpace != "bt709" {
			return "", false, errUnsupportedStream
		}
		peak := "100"
		if stream.ColorTransfer == "arib-std-b67" {
			peak = "10"
		}
		// tonemap needs BT.709 luminance coefficients even though its pixels
		// are float RGB. Restore the RGB input matrix explicitly for zscale.
		filter += ",zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,setparams=colorspace=bt709,tonemap=mobius:param=0.3:desat=2:peak=" + peak + ",zscale=t=bt709:m=bt709:matrixin=gbr:r=limited,format=yuv420p"
	}
	return filter, hdr, nil
}

func hdrOutputColorArgs(hdr bool) []string {
	if !hdr {
		return nil
	}
	return []string{"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-bsf:v", "h264_metadata=colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1:video_full_range_flag=0"}
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
	return validateRenderedVideoWithCrop(info, source, quality, preview, hdr, nil)
}

func validateRenderedVideoWithCrop(info probeInfo, source *probeStream, quality string, preview, hdr bool, crop *Crop) error {
	video := info.selectedVideo()
	width, height, err := videoOutputGeometryWithCrop(source, quality, preview, crop)
	if err != nil {
		return err
	}
	if video == nil || video.Width != width || video.Height != height || video.SampleAspectRatio != "1:1" || math.Abs(video.rotation()) > 0.001 {
		return fmt.Errorf("result video does not match the display geometry")
	}
	if hdr && (video.ColorPrimaries != "bt709" || video.ColorTransfer != "bt709" || video.ColorSpace != "bt709" || video.ColorRange != "tv" || video.PixelFormat != "yuv420p") {
		return fmt.Errorf("result video does not match the SDR color contract")
	}
	return nil
}
