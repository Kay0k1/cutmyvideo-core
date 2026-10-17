package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/coremodel"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

var _ func(*coremodel.ExportRequest, Config, Source) error = (*ExportRequest).Validate
var _ Config = coremodel.Config{}
var _ Source = coremodel.Source{Fingerprint: &SourceFingerprint{}}

func TestCoreModelAndMediaErrorAdaptersPreserveLegacyContracts(t *testing.T) {
	if ErrSchemaIncompatible != coremodel.ErrSchemaIncompatible || ErrStorageInitializing != coremodel.ErrStorageInitializing {
		t.Fatal("shared refusal sentinel identity changed")
	}
	if reflect.TypeOf(ExportRequest{}) != reflect.TypeOf(coremodel.ExportRequest{}) || reflect.TypeOf(Source{}) != reflect.TypeOf(coremodel.Source{}) {
		t.Fatal("compatibility aliases lost type identity")
	}
	raw := errors.New("opaque callback cause")
	for _, sample := range []struct{ neutral, legacy error }{
		{mediaerror.ErrNoAudio, errNoAudio}, {mediaerror.ErrOutputLimit, errOutputLimit}, {mediaerror.ErrUnsupportedStream, errUnsupportedStream}, {mediaerror.ErrProcessResponseTooLarge, errProcessResponseTooLarge},
	} {
		if localMediaError(sample.neutral) != sample.legacy {
			t.Fatal("sentinel identity changed", sample.neutral)
		}
	}
	legacyProcess := &mediaProcessFailure{cause: raw, category: "copy_incompatible"}
	legacyInspection := &localInspectionFailure{cause: raw}
	legacyCopy := copyExportDiagnostic(legacyProcess, "mp4")
	for _, err := range []error{errNoAudio, errOutputLimit, errUnsupportedStream, legacyProcess, legacyInspection, legacyCopy, fmt.Errorf("outer: %w", legacyCopy), errors.Join(&sourceProblem{code: "unknown", message: "ignored"}, legacyProcess)} {
		if got, want := mediaerror.LocalErrorCode(err), LocalErrorCode(err); got != want {
			t.Fatalf("legacy classifier projection %v: %q want %q", err, got, want)
		}
		if localMediaError(err) != err {
			t.Fatal("existing app error identity changed", err)
		}
	}
	// Pin the prior app classifier priority independently of its neutral delegate.
	for _, sample := range []struct {
		err  error
		want string
	}{
		{nil, ""}, {raw, ""}, {errors.New("audio_missing"), ""},
		{&sourceProblem{code: "crop_invalid", message: "crop"}, "invalid_argument"},
		{errUnsupportedStream, "unsupported_media"},
		{&mediaProcessFailure{cause: errNoAudio, category: "storage_full"}, "audio_missing"},
		{&mediaProcessFailure{cause: raw, category: "unknown"}, "processing_failed"},
		{&mediaProcessFailure{cause: raw, category: "missing_audio"}, "audio_missing"},
		{&localInspectionFailure{cause: legacyProcess}, "copy_incompatible"},
		{legacyInspection, "unsupported_media"},
		{errors.Join(&sourceProblem{code: "unknown", message: "ignored"}, legacyProcess), "copy_incompatible"},
	} {
		if got := LocalErrorCode(sample.err); got != sample.want {
			t.Fatalf("legacy classification %v: %q want %q", sample.err, got, sample.want)
		}
	}
	neutralProcess := &mediaerror.ProcessFailure{Cause: raw, Category: "copy_incompatible"}
	converted := localMediaError(neutralProcess)
	process, ok := converted.(*mediaProcessFailure)
	if !ok || process.cause != raw || process.category != "copy_incompatible" || converted.Error() != neutralProcess.Error() {
		t.Fatal("private process shape changed", converted)
	}
	converted = localMediaError(mediaerror.CopyDiagnostic(neutralProcess, "mp4"))
	copy, ok := converted.(*copyExportFailure)
	if !ok || copy.problem.code != "copy_incompatible" || !errors.Is(copy, raw) || copy.Error() != legacyCopy.Error() {
		t.Fatal("copy diagnostic shape changed", converted)
	}
	if mediaerror.CopyDiagnostic(legacyCopy, "mkv") != legacyCopy {
		t.Fatal("projected legacy copy diagnostic not idempotent")
	}
	neutralHDR := &mediaerror.ProcessFailure{Cause: raw, Category: "hdr_conversion_unavailable"}
	converted = localMediaError(mediaerror.RenderedFailure(neutralHDR, true))
	legacyHDR := renderedMediaFailure(&mediaProcessFailure{cause: raw, category: "hdr_conversion_unavailable"}, true)
	if reflect.TypeOf(converted) != reflect.TypeOf(legacyHDR) || converted.Error() != legacyHDR.Error() || !errors.Is(converted, raw) || LocalErrorCode(converted) != "hdr_conversion_unavailable" {
		t.Fatal("render wrapper shape changed", converted)
	}
	var problem *sourceProblem
	if !errors.As(converted, &problem) || problem.code != "hdr_conversion_unavailable" {
		t.Fatal("render diagnostic lost legacy problem", converted)
	}
	opaque := fmt.Errorf("opaque wrapper: %w", raw)
	for _, err := range []error{nil, raw, opaque, context.Canceled, context.DeadlineExceeded} {
		if localMediaError(err) != err {
			t.Fatal("opaque callback identity changed", err)
		}
	}
}
