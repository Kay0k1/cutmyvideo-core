package mediainfo

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

// Display dimensions describe square pixels after applying the container's
// right-angle display rotation. Coded Width/Height keep their original meaning.
func DisplayGeometry(stream *Stream) (int, int, error) {
	if stream == nil {
		return 0, 0, nil
	}
	sar := 1.0
	if value := stream.SampleAspectRatio; value != "" && value != "N/A" && value != "0:1" {
		parts := strings.Split(value, ":")
		if len(parts) != 2 {
			return 0, 0, ErrUnsupported
		}
		numerator, err := strconv.ParseInt(parts[0], 10, 32)
		denominator, denomErr := strconv.ParseInt(parts[1], 10, 32)
		if err != nil || denomErr != nil || numerator <= 0 || denominator <= 0 {
			return 0, 0, ErrUnsupported
		}
		sar = float64(numerator) / float64(denominator)
	}
	width, height := math.Round(float64(stream.Width)*sar), float64(stream.Height)
	rotation := math.Mod(stream.Rotation(), 360)
	if rotation < 0 {
		rotation += 360
	}
	quarterTurns := math.Round(rotation / 90)
	if math.Abs(rotation-quarterTurns*90) > 0.001 {
		return 0, 0, ErrUnsupported
	}
	if int(quarterTurns)%2 != 0 {
		width, height = height, width
	}
	if width < 2 || height < 2 || math.IsNaN(width) || math.IsInf(width, 0) || width > float64(MaxPixels)/height {
		return 0, 0, ErrUnsupported
	}
	return int(width), int(height), nil
}

func OutputGeometry(stream *Stream, quality string, preview bool) (int, int, error) {
	return OutputGeometryWithCrop(stream, quality, preview, nil)
}

func OutputGeometryWithCrop(stream *Stream, quality string, preview bool, crop *edit.Crop) (int, int, error) {
	width, height, err := DisplayGeometry(stream)
	if err != nil || stream == nil {
		return 0, 0, ErrUnsupported
	}
	if crop != nil {
		_, _, width, height, err = edit.CropPixelGeometry(width, height, crop)
		if err != nil {
			return 0, 0, &Problem{"crop_invalid", err.Error()}
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
	// Avoid losing two pixels when an exact cap such as 480 is represented as
	// 479.99999999999994 after multiplication. This epsilon is far below one
	// displayed pixel and does not round genuine fractional dimensions up.
	return max(2, int(math.Floor(float64(width)*factor+1e-9))/2*2), max(2, int(math.Floor(float64(height)*factor+1e-9))/2*2), nil
}

func EncoderTimeBase(stream *Stream) (string, error) {
	parts := strings.Split(stream.TimeBase, "/")
	if len(parts) != 2 {
		return "", ErrUnsupported
	}
	numerator, err := strconv.ParseInt(parts[0], 10, 32)
	denominator, denominatorErr := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || denominatorErr != nil || numerator <= 0 || denominator <= 0 {
		return "", ErrUnsupported
	}
	// An explicit positive ratio works across supported FFmpeg versions.
	// Historical "-1" and newer "demux" spellings differ between releases.
	return fmt.Sprintf("%d/%d", numerator, denominator), nil
}

func RenderFilter(stream *Stream, quality string, preview bool) (string, bool, error) {
	width, height, err := OutputGeometry(stream, quality, preview)
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
	return ColorRenderFilter(stream, filter)
}

func ColorRenderFilter(stream *Stream, filter string) (string, bool, error) {
	hdr := stream.ColorTransfer == "smpte2084" || stream.ColorTransfer == "arib-std-b67"
	if hdr {
		// PQ/HLG must be linearized before tone mapping. npl sets the 100-nit
		// SDR reference white; use the PQ legal peak or HLG reference peak.
		// Missing/contradictory colorimetry is not replaced by guessed tags.
		if stream.ColorPrimaries != "bt2020" && stream.ColorPrimaries != "bt709" ||
			stream.ColorSpace != "bt2020nc" && stream.ColorSpace != "bt2020c" && stream.ColorSpace != "bt709" {
			return "", false, ErrUnsupported
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

func HDROutputColorArgs(hdr bool) []string {
	if !hdr {
		return nil
	}
	return []string{"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-bsf:v", "h264_metadata=colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1:video_full_range_flag=0"}
}

func ValidateRenderedVideo(info Info, source *Stream, quality string, preview, hdr bool) error {
	return ValidateRenderedVideoWithCrop(info, source, quality, preview, hdr, nil)
}

func ValidateRenderedVideoWithCrop(info Info, source *Stream, quality string, preview, hdr bool, crop *edit.Crop) error {
	video := info.SelectedVideo()
	width, height, err := OutputGeometryWithCrop(source, quality, preview, crop)
	if err != nil {
		return err
	}
	if video == nil || video.Width != width || video.Height != height || video.SampleAspectRatio != "1:1" || math.Abs(video.Rotation()) > 0.001 {
		return fmt.Errorf("result video does not match the display geometry")
	}
	if hdr && (video.ColorPrimaries != "bt709" || video.ColorTransfer != "bt709" || video.ColorSpace != "bt709" || video.ColorRange != "tv" || video.PixelFormat != "yuv420p") {
		return fmt.Errorf("result video does not match the SDR color contract")
	}
	return nil
}
