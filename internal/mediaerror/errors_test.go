package mediaerror

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestMediaErrorPreservesClassificationAndCauses(t *testing.T) {
	raw := errors.New("opaque subprocess diagnostic")
	process := &ProcessFailure{Cause: raw, Category: "copy_incompatible"}
	for _, sample := range []struct {
		err  error
		want string
	}{
		{nil, ""}, {raw, ""}, {errors.New("unsupported_stream"), ""},
		{ErrNoAudio, "audio_missing"}, {ErrOutputLimit, "output_limit"}, {ErrUnsupportedStream, "unsupported_media"},
		{&Problem{Code: "crop_invalid", Message: "crop"}, "invalid_argument"},
		{&Problem{Code: "target_size_unattainable", Message: "bounded target"}, "target_size_unattainable"},
		{&Problem{Code: "copy_audio_unsupported", Message: "AAC required"}, "copy_audio_unsupported"},
		{process, "copy_incompatible"}, {&ProcessFailure{Cause: raw, Category: "unknown"}, "processing_failed"},
		{&ProcessFailure{Cause: ErrNoAudio, Category: "storage_full"}, "audio_missing"},
		{&InspectionFailure{Cause: process}, "copy_incompatible"}, {&InspectionFailure{Cause: raw}, "unsupported_media"},
		{errors.Join(&Problem{Code: "unknown", Message: "ignored"}, process), "copy_incompatible"},
	} {
		if got := LocalErrorCode(sample.err); got != sample.want {
			t.Fatalf("classification %v: %q want %q", sample.err, got, sample.want)
		}
	}
	for _, format := range []string{"mp4", "mkv", "other"} {
		diagnostic := CopyDiagnostic(process, format)
		var copy *CopyFailure
		if !errors.As(diagnostic, &copy) || copy.Cause != process || !errors.Is(diagnostic, raw) || LocalErrorCode(diagnostic) != "copy_incompatible" {
			t.Fatal("copy lost cause or code", diagnostic)
		}
		wrapped := fmt.Errorf("outer: %w", diagnostic)
		if CopyDiagnostic(wrapped, format) != wrapped {
			t.Fatal("copy diagnostic is no longer idempotent")
		}
	}
	if CopyDiagnostic(context.Canceled, "mp4") != context.Canceled {
		t.Fatal("opaque cancellation replaced")
	}
	hdr := &ProcessFailure{Cause: raw, Category: "hdr_conversion_unavailable"}
	rendered := RenderedFailure(hdr, true)
	if rendered.Error() != "HDR conversion to SDR is unavailable with the configured media tools: "+hdr.Error() || !errors.Is(rendered, raw) || LocalErrorCode(rendered) != "hdr_conversion_unavailable" {
		t.Fatal("HDR wrapper or cause changed", rendered)
	}
	if RenderedFailure(hdr, false) != hdr || RenderedFailure(process, true) != process {
		t.Fatal("unrelated render error replaced")
	}
	if ErrNoAudio.Error() != "This video has no audio track. Export it as MP4 video instead" || ErrOutputLimit.Error() != "The result exceeds the output size limit. Choose a shorter fragment or lower quality" || ErrUnsupportedStream.Error() != "This video uses unsupported encryption or media streams. Upload an accessible file instead" {
		t.Fatal("fixed diagnostic changed")
	}
}
