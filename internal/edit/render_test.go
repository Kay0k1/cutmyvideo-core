package edit

import (
	"encoding/json"
	"math"
	"testing"
)

func TestRenderValidationAndCollectionIsolation(t *testing.T) {
	i, tp, lra := -16.0, 0.0, 11.0
	normalize := &AudioNormalize{IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: &lra}
	base := ExportRequest{Ranges: []Range{{EndMS: 1000}}, Format: "mp4", Quality: "best", CutMode: "accurate", AudioNormalize: normalize, Canvas: &Canvas{Width: 180, Height: 320, Fit: "contain", Background: "#123456"}, AudioReplace: &AudioReplace{Input: "audio.wav"}}
	limits := Limits{MaxRanges: 128, MaxRangeMS: 4000, MaxJobMS: 4000}
	source := Source{DurationMS: 4000, HasLocalMedia: true}
	if err := ValidateExportRequest(&base, limits, source); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []*AudioNormalize{{IntegratedLUFS: &i, LoudnessRangeLU: &lra}, {IntegratedLUFS: &i, TruePeakDBTP: &tp, LoudnessRangeLU: nil}, {IntegratedLUFS: func() *float64 { x := math.NaN(); return &x }(), TruePeakDBTP: &tp, LoudnessRangeLU: &lra}} {
		bad := base
		bad.AudioNormalize = invalid
		if ValidateExportRequest(&bad, limits, source) == nil {
			t.Fatal("missing/nonfinite loudness target accepted")
		}
	}
	plan := CollectionPlan{Version: 1, Options: CollectionOptions{AudioNormalize: normalize, Canvas: base.Canvas, AudioReplace: base.AudioReplace}, Clips: []CollectionClip{{ID: "a", Ranges: base.Ranges}, {ID: "b", Ranges: base.Ranges, Options: CollectionOptions{ClearCanvas: true, ClearAudioNormalize: true, ClearAudioReplace: true}}}}
	clips, err := NormalizeCollection(plan, CollectionExecution{}, limits, source)
	if err != nil {
		t.Fatal(err)
	}
	if clips[0].Request.Canvas == nil || clips[0].Request.AudioNormalize == nil || clips[0].Request.AudioReplace == nil || clips[1].Request.Canvas != nil || clips[1].Request.AudioNormalize != nil || clips[1].Request.AudioReplace != nil {
		t.Fatal("render option inheritance/clear failed", clips)
	}
	clips[0].Request.Canvas.Width = 10
	*clips[0].Request.AudioNormalize.TruePeakDBTP = -4
	clips[0].Request.AudioReplace.Input = "changed.wav"
	if base.Canvas.Width != 180 || tp != 0 || base.AudioReplace.Input != "audio.wav" {
		t.Fatal("normalization mutated caller-owned nested options")
	}
	encoded, err := json.Marshal(ExportRequest{})
	if err != nil || string(encoded) != `{"source_id":"","ranges":null,"format":"","quality":"","cut_mode":""}` {
		t.Fatal("legacy zero request JSON changed", string(encoded), err)
	}
}
