package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediapreflight"
)

func TestMediaPreflightBridgePreservesLegacyShapesAndCauses(t *testing.T) {
	// The public engine aliases this request, including the legacy method.
	var _ interface{ Validate(Config, Source) error } = (*ExportRequest)(nil)
	request := preflightTestRequest()
	request.AudioTrack = new(int)
	*request.AudioTrack = 1
	request.Crop = &Crop{Width: 1, Height: 1}
	for _, original := range []PreflightResult{
		{},
		{Version: 1, Request: request},
		{Version: 1, Request: request, TrackManifest: []TrackSelection{}, Checks: []PreflightCheck{}, Warnings: []PreflightWarning{}},
	} {
		roundTrip := mediaPreflightResult(mediaPreflightReport(original))
		before, _ := json.Marshal(original)
		after, _ := json.Marshal(roundTrip)
		if !reflect.DeepEqual(original, roundTrip) || string(before) != string(after) || roundTrip.Request.AudioTrack != original.Request.AudioTrack || roundTrip.Request.Crop != original.Request.Crop {
			t.Fatal("preflight bridge changed tags, empty arrays or request ownership", string(before), string(after))
		}
	}
	for _, groups := range [][]preflightCapabilityGroup{nil, {}, {{listing: "encoders", code: "encoder_unavailable", required: []string{"libx264"}}}} {
		if got := preflightGroups(mediaPreflightGroups(groups)); !reflect.DeepEqual(got, groups) {
			t.Fatal("capability projection changed nil, order or requirements", got, groups)
		}
	}
	var report PreflightResult
	err := applyPreflightListing(preflightCapabilityGroup{listing: "encoders", code: "encoder_unavailable", required: []string{"libx264"}}, nil, false, &report)
	var escaped *mediainfo.Problem
	problem, ok := err.(*sourceProblem)
	if !ok || problem.code != "encoder_unavailable" || errors.As(err, &escaped) || errors.Unwrap(err) != nil || report.Checks != nil || report.Warnings != nil {
		t.Fatal("capability failure changed legacy error shape or partial report", report, err)
	}
	if _, err := validatePreflightSelection(mediaSelection{}, ExportRequest{Format: "mp3"}); err != errNoAudio {
		t.Fatal("preflight decision lost missing-audio sentinel identity", err)
	}
	if _, err := validatePreflightSelection(mediaSelection{}, ExportRequest{Format: "mp4"}); err != errUnsupportedStream {
		t.Fatal("preflight decision lost unsupported-media sentinel identity", err)
	}
	c := Config{MaxRanges: 128, MaxRangeMS: 3600000, MaxJobMS: 3600000}
	got, groups, err := prepareLocalPreflight(c, "source.mp4", request, preflightTestInfo(), 2000)
	neutral, neutralGroups, neutralErr := mediapreflight.Prepare(editLimits(c), "source.mp4", edit.ExportRequest(request), mediainfo.Info(preflightTestInfo()), 2000)
	if err != nil || neutralErr != nil || !reflect.DeepEqual(got, mediaPreflightResult(neutral)) || !reflect.DeepEqual(groups, preflightGroups(neutralGroups)) || &got.Request.Ranges[0] == &request.Ranges[0] || got.Request.AudioTrack == request.AudioTrack || got.Request.Crop == request.Crop {
		t.Fatal("app report differs from the shared plan or aliases caller state", got, err, neutralErr)
	}
	for _, callbackError := range []error{
		fmt.Errorf("source check: %w", context.Canceled),
		&sourceProblem{"source_changed", "fixed legacy source failure"},
		fmt.Errorf("wrapped decision: %w", &mediainfo.Problem{Code: "invalid_argument", Message: "opaque callback failure"}),
	} {
		if got := mediaExportError(callbackError); got != callbackError {
			t.Fatal("preflight callback cause was replaced", got, callbackError)
		}
	}
}
