package subtitles

import (
	"context"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func TestRetimeVersion2ScalesWhileVersion1KeepsClamping(t *testing.T) {
	request := RetimeRequest{Version: 1, Format: "srt", Text: "1\n00:00:01,000 --> 00:00:03,000\nSpeech\n", Outputs: []RetimeClip{{ClipID: "clip", Segments: []edit.Segment{{SourceEndMS: 4000, OutputEndMS: 2000}}}}}
	legacy, err := Retime(context.Background(), request)
	if err != nil || len(legacy.Outputs) != 1 || !strings.Contains(legacy.Outputs[0].Text, "00:00:01,000 --> 00:00:02,000") {
		t.Fatal("version1 no longer translates and clamps", legacy, err)
	}
	request.Version = 2
	scaled, err := Retime(context.Background(), request)
	if err != nil || scaled.Version != 1 || len(scaled.Outputs) != 1 || !strings.Contains(scaled.Outputs[0].Text, "00:00:00,500 --> 00:00:01,500") {
		t.Fatal("version2 did not scale the complete cue", scaled, err)
	}
	request.Outputs[0].Segments = []edit.Segment{{SourceStartMS: 2000, SourceEndMS: 4000, OutputEndMS: 4000}, {SourceStartMS: 0, SourceEndMS: 2000, OutputStartMS: 4000, OutputEndMS: 5000}, {SourceStartMS: 2000, SourceEndMS: 4000, OutputStartMS: 5000, OutputEndMS: 6000}}
	scaled, err = Retime(context.Background(), request)
	if err != nil || scaled.Outputs[0].CueCount != 3 {
		t.Fatal("scaled reordered/repeated maps lost clipped cues", scaled, err)
	}
	for _, interval := range []string{"00:00:00,000 --> 00:00:02,000", "00:00:04,500 --> 00:00:05,000", "00:00:05,000 --> 00:00:05,500"} {
		if !strings.Contains(scaled.Outputs[0].Text, interval) {
			t.Fatal("scaled half-open boundary differs", interval, scaled.Outputs[0].Text)
		}
	}
	request.Text = "1\n00:00:00,001 --> 00:00:00,002\nBoundary\n"
	request.Outputs[0].Segments = []edit.Segment{{SourceEndMS: maxSubtitleClockMS, OutputEndMS: maxSubtitleClockMS}}
	limit, err := Retime(context.Background(), request)
	if err != nil || limit.Outputs[0].CueCount != 1 || !strings.Contains(limit.Outputs[0].Text, "00:00:00,001 --> 00:00:00,002") {
		t.Fatal("maximum legal clock overflowed", limit, err)
	}
}
