package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func TestMediaInfoErrorBridgePreservesLegacyTypesAndCauses(t *testing.T) {
	if mediaInfoError(nil) != nil || mediaInfoError(mediainfo.ErrUnsupported) != errUnsupportedStream {
		t.Fatal("media error bridge changed nil or unsupported sentinel identity")
	}
	for _, test := range []struct{ code, localCode string }{
		{"crop_invalid", "invalid_argument"},
		{"audio_track_unavailable", "audio_track_unavailable"},
		{"subtitle_unsupported", "subtitle_unsupported"},
	} {
		leaf := &mediainfo.Problem{Code: test.code, Message: "fixed diagnostic"}
		err := mediaInfoError(leaf)
		var problem *sourceProblem
		var escaped *mediainfo.Problem
		if !errors.As(err, &problem) || problem.code != test.code || problem.message != leaf.Message || err.Error() != leaf.Message || LocalErrorCode(err) != test.localCode {
			t.Fatal("shared validation lost its legacy typed diagnostic", test.code, err)
		}
		if errors.As(err, &escaped) || errors.Unwrap(err) != nil {
			t.Fatal("adapter introduced a new diagnostic cause into the legacy chain", err)
		}
	}
	for _, original := range []error{
		errors.New("ordinary validation"),
		&sourceProblem{"audio_missing", "fixed legacy diagnostic"},
		fmt.Errorf("cancelled inspection: %w", context.Canceled),
		fmt.Errorf("inspection deadline: %w", context.DeadlineExceeded),
		&os.PathError{Op: "open", Path: "fixture", Err: os.ErrPermission},
	} {
		if got := mediaInfoError(original); got != original || !errors.Is(got, original) {
			t.Fatal("media error bridge replaced an existing error or cause", original)
		}
	}
}

func TestMediaInfoGeometryPreservesValidationOrder(t *testing.T) {
	stream := &probeStream{Width: 100, Height: 100, SampleAspectRatio: "invalid"}
	crop := &Crop{Width: 2, Height: 1}
	if _, _, err := videoOutputGeometryWithCrop(stream, "best", false, crop); err != errUnsupportedStream {
		t.Fatal("unsupported display geometry no longer precedes crop validation", err)
	}
	stream.SampleAspectRatio = "1:1"
	// Crop validation precedes HDR color validation in the render filter.
	stream.ColorTransfer = "smpte2084"
	if _, _, err := videoCropRenderFilter(stream, "best", crop); err == nil {
		t.Fatal("invalid crop was accepted")
	} else {
		var problem *sourceProblem
		if !errors.As(err, &problem) || problem.code != "crop_invalid" || LocalErrorCode(err) != "invalid_argument" {
			t.Fatal("crop diagnostic was replaced by a later HDR failure", err)
		}
	}
	if _, _, err := videoCropRenderFilter(stream, "best", nil); err != errUnsupportedStream {
		t.Fatal("invalid HDR color geometry lost the unsupported sentinel", err)
	}
	if width, height, err := videoDisplayGeometry(nil); width != 0 || height != 0 || err != nil {
		t.Fatal("empty display geometry changed", width, height, err)
	}
	if _, _, err := videoOutputGeometry(nil, "best", false); err != errUnsupportedStream {
		t.Fatal("empty output geometry lost its unsupported diagnostic", err)
	}
	stream.ColorTransfer = ""
	output := probeInfo{Streams: []probeStream{{CodecType: "video", Width: 98, Height: 100, SampleAspectRatio: "1:1"}}}
	err := validateRenderedVideo(output, stream, "best", false, true)
	var problem *sourceProblem
	if err == nil || err.Error() != "result video does not match the display geometry" || errors.As(err, &problem) || LocalErrorCode(err) != "" {
		t.Fatal("rendered geometry mismatch acquired a typed diagnostic or lost precedence", err)
	}
	output.Streams[0].Width = 100
	err = validateRenderedVideo(output, stream, "best", false, true)
	if err == nil || err.Error() != "result video does not match the SDR color contract" || errors.As(err, &problem) || LocalErrorCode(err) != "" {
		t.Fatal("rendered color mismatch acquired a typed diagnostic", err)
	}
}
