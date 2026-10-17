package mediapreflight

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func preflightFixture() (edit.Limits, mediainfo.Info, edit.ExportRequest) {
	limits := edit.Limits{MaxRanges: 128, MaxRangeMS: 3600000, MaxJobMS: 3600000}
	info := mediainfo.Info{Streams: []mediainfo.Stream{
		{Index: 0, CodecType: "video", CodecName: "h264", Width: 160, Height: 90, TimeBase: "1/12800", SampleAspectRatio: "1:1"},
		{Index: 1, CodecType: "audio", CodecName: "aac", Channels: 2, SampleRate: "48000", TimeBase: "1/48000"},
	}}
	audio := 1
	request := edit.ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "separate", SubtitleMode: "omit",
		Ranges: []edit.Range{{StartMS: 500, EndMS: 1500, Label: "  independent clip  "}}, AudioTrack: &audio, Crop: &edit.Crop{Width: 1, Height: 1}}
	return limits, info, request
}

func TestPreflightPlanPreservesRequestOwnershipAndDecisionOrder(t *testing.T) {
	limits, info, request := preflightFixture()
	before, _ := json.Marshal(request)
	first, groups, err := Prepare(limits, "source.mp4", request, info, 2000)
	if err != nil || len(groups) != 5 || first.Request.Ranges[0].Label != "independent clip" || first.Request.OutputMode != "" || first.Request.SubtitleMode != "" || first.RequestedMS != 1000 || first.OutputCount != 1 {
		t.Fatal("preflight request normalization changed", first, groups, err)
	}
	second, _, err := Prepare(limits, "source.mp4", request, info, 2000)
	if err != nil || first.Request.AudioTrack == request.AudioTrack || first.Request.Crop == request.Crop || first.TrackManifest == nil || first.Checks == nil || first.Warnings == nil {
		t.Fatal("preflight lost report ownership or empty-array shapes", err)
	}
	first.Request.Ranges[0].Label = "changed"
	*first.Request.AudioTrack = 99
	first.Request.Crop.Width = .5
	first.Video.Width = -1
	first.TrackManifest[0].Included = false
	first.Checks[0].Status = "changed"
	after, _ := json.Marshal(request)
	if string(before) != string(after) || second.Request.Ranges[0].Label != "independent clip" || *second.Request.AudioTrack != 1 || second.Request.Crop.Width != 1 || second.Video.Width < 0 || !second.TrackManifest[0].Included || second.Checks[0].Status != "passed" {
		t.Fatal("normalization or reports shared caller-owned mutable fields")
	}
	request.Crop.Width = 2
	missing := 99
	request.AudioTrack = &missing
	_, _, err = Prepare(limits, "source.mp4", request, info, 2000)
	var problem *mediainfo.Problem
	if !errors.As(err, &problem) || problem.Code != "invalid_argument" {
		t.Fatal("invalid request no longer precedes media stream selection", err)
	}
	video := &mediainfo.Stream{Width: 160, Height: 90, SampleAspectRatio: "invalid"}
	if _, err := ValidateSelection(mediaexport.Selection{Video: video}, edit.ExportRequest{Format: "mp4", CutMode: "accurate", Quality: "best", Crop: &edit.Crop{Width: 2, Height: 1}}); err != mediainfo.ErrUnsupported {
		t.Fatal("unsupported display metadata no longer precedes crop geometry", err)
	}
	if _, err := ValidateSelection(mediaexport.Selection{}, edit.ExportRequest{Format: "mp3"}); err != mediaexport.ErrNoAudio {
		t.Fatal("missing audio lost its neutral sentinel", err)
	}
	copyReport := Result{Request: edit.ExportRequest{CutMode: "copy"}}
	Finish(&copyReport)
	want := []string{"copy_boundaries", "copy_container", "media_decode", "output_size", "storage", "publication"}
	var names []string
	for _, check := range copyReport.Checks {
		names = append(names, check.Name)
		if check.Status != "deferred" {
			t.Fatal("unexecuted work was certified", check)
		}
	}
	if !reflect.DeepEqual(names, want) || len(copyReport.Warnings) != len(want) {
		t.Fatal("deferred check order changed", copyReport)
	}
}

func TestPreflightCapabilitiesPreserveExactTablesAndRequirements(t *testing.T) {
	data := []byte("Encoders:\n V..... = Video\n ------\n V..... libx264 description mentions libx265\n A..... aac AAC encoder\n")
	available, parsed := ParseCapabilityList("encoders", data)
	if !parsed || !available["libx264"] || available["libx265"] {
		t.Fatal("descriptive names became capabilities", available, parsed)
	}
	for _, invalid := range [][]byte{
		append(append([]byte(nil), data...), []byte("truncated row\n")...),
		[]byte("Encoders:\n V..... libx264 missing separator\n"),
		[]byte(strings.Repeat("x", CapabilityBytes+1)),
	} {
		if _, ok := ParseCapabilityList("encoders", invalid); ok {
			t.Fatal("unverified or oversized listing was certified")
		}
	}
	selection := mediaexport.Selection{Video: &mediainfo.Stream{}, Audio: &mediainfo.Stream{}}
	groups := RequiredCapabilities(selection, edit.ExportRequest{Format: "mp4", CutMode: "accurate", OutputMode: "joined", Crop: &edit.Crop{}}, nil, &Video{HDRToSDR: true})
	var listings []string
	for _, group := range groups {
		listings = append(listings, group.Listing)
	}
	if !reflect.DeepEqual(listings, []string{"encoders", "muxers", "filters", "demuxers", "bsfs"}) ||
		!reflect.DeepEqual(groups[2].Required, []string{"scale", "setsar", "crop", "zscale", "format", "setparams", "tonemap", "select", "aselect", "aresample"}) ||
		!reflect.DeepEqual(groups[4].Required, []string{"h264_metadata"}) {
		t.Fatal("joined/HDR capability requirements or group order changed", groups)
	}
	groups = RequiredCapabilities(selection, edit.ExportRequest{Format: "mp4", CutMode: "copy", OutputMode: "joined"}, []edit.TrackSelection{{Track: edit.Track{Type: "subtitle"}, Included: true}}, nil)
	if !reflect.DeepEqual(groups[0].Required, []string{"subrip", "mov_text"}) ||
		!reflect.DeepEqual(groups[1].Required, []string{"mp4", "matroska", "srt"}) ||
		!reflect.DeepEqual(groups[3].Required, []string{"concat", "mov", "matroska", "srt", "mov"}) || groups[2].Required != nil || groups[4].Required != nil {
		t.Fatal("copy/subtitle intermediate capability requirements changed", groups)
	}
	group := CapabilityGroup{Listing: "encoders", Code: "encoder_unavailable", Required: []string{"libx264", "aac"}}
	var report Result
	if err := ApplyListing(group, nil, true, &report); err != nil || len(report.Checks) != 1 || report.Checks[0].Status != "deferred" || report.Warnings[0].Code != "tool_capabilities_unverified" {
		t.Fatal("unverified listing was not deferred", report, err)
	}
	err := ApplyListing(group, map[string]bool{"aac": true}, false, &report)
	var problem *mediainfo.Problem
	if !errors.As(err, &problem) || problem.Code != "encoder_unavailable" || !strings.HasSuffix(problem.Message, "capability libx264") || len(report.Checks) != 1 {
		t.Fatal("missing capability precedence or partial report changed", report, err)
	}
}

type preflightBatchRuntime struct {
	unchanged func(context.Context) error
	check     func(context.Context, []CapabilityGroup, *Result, map[string]map[string]bool) error
}

func (runtime preflightBatchRuntime) Unchanged(ctx context.Context) error {
	return runtime.unchanged(ctx)
}

func (runtime preflightBatchRuntime) Check(ctx context.Context, groups []CapabilityGroup, report *Result, wanted map[string]map[string]bool) error {
	return runtime.check(ctx, groups, report, wanted)
}

func TestPreflightBatchPreservesUnionAndFailureOrder(t *testing.T) {
	limits, info, request := preflightFixture()
	requests := []edit.ExportRequest{request, request}
	requests[0].Crop = nil
	requests[1].Crop = &edit.Crop{X: .25, Width: .5, Height: 1}
	var calls []string
	runtime := preflightBatchRuntime{
		unchanged: func(context.Context) error { calls = append(calls, "unchanged"); return nil },
		check: func(_ context.Context, _ []CapabilityGroup, _ *Result, wanted map[string]map[string]bool) error {
			calls = append(calls, "check")
			if !wanted["encoders"]["libx264"] || !wanted["filters"]["crop"] || wanted["encoders"]["unrequested"] {
				t.Fatal("batch lost its exact selected capability union", wanted)
			}
			return nil
		},
	}
	reports, failed, err := Batch(context.Background(), limits, "source.mp4", requests, info, 2000, runtime)
	wantCalls := []string{"unchanged", "unchanged", "unchanged", "unchanged", "unchanged", "check", "unchanged", "unchanged", "check", "unchanged", "unchanged"}
	if err != nil || failed != -1 || len(reports) != 2 || !reflect.DeepEqual(calls, wantCalls) {
		t.Fatal("batch changed source fences or check order", calls, failed, err)
	}
	reports[0].Request.Ranges[0].Label = "changed"
	reports[0].Checks[0].Status = "changed"
	if reports[1].Request.Ranges[0].Label != "independent clip" || reports[1].Checks[0].Status != "passed" {
		t.Fatal("batch reports shared mutable state")
	}
	missingAudio := 99
	requests[1].AudioTrack = &missingAudio
	callbackFailure := errors.New("earlier capability failure")
	checks := 0
	runtime.check = func(context.Context, []CapabilityGroup, *Result, map[string]map[string]bool) error {
		checks++
		return callbackFailure
	}
	if reports, failed, err := Batch(context.Background(), limits, "source.mp4", requests, info, 2000, runtime); reports != nil || failed != 0 || err != callbackFailure || checks != 1 {
		t.Fatal("later request validation hid an earlier capability failure", reports, failed, err, checks)
	}
	runtime.check = func(context.Context, []CapabilityGroup, *Result, map[string]map[string]bool) error { return nil }
	if reports, failed, err := Batch(context.Background(), limits, "source.mp4", requests, info, 2000, runtime); reports != nil || failed != 1 {
		t.Fatal("later invalid request lost its index", reports, failed, err)
	} else {
		var problem *mediainfo.Problem
		if !errors.As(err, &problem) || problem.Code != "audio_track_unavailable" {
			t.Fatal("later media failure changed its diagnostic", err)
		}
	}
	fences := 0
	runtime.unchanged = func(context.Context) error {
		fences++
		if fences == 6 {
			return context.Canceled
		}
		return nil
	}
	if reports, failed, err := Batch(context.Background(), limits, "source.mp4", requests, info, 2000, runtime); reports != nil || failed != 0 || err != context.Canceled {
		t.Fatal("source fence failure lost identity or precedence", reports, failed, err)
	}
}
