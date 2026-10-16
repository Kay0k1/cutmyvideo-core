package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func subtitleTestRequest(text, format string) SubtitleRetimeRequest {
	return SubtitleRetimeRequest{Version: 1, Format: format, Text: text, Outputs: []SubtitleRetimeClip{{ClipID: "opening", Segments: []ArtifactSegment{{SourceStartMS: 1000, SourceEndMS: 4000, OutputStartMS: 0, OutputEndMS: 3000}}}}}
}

func TestRetimeSubtitlesActualMapsAndRepeatedReorderedCues(t *testing.T) {
	request := subtitleTestRequest("1\n00:00:05,000 --> 00:00:08,000\nВопрос\nи ответ\n\n2\n00:00:00,500 --> 00:00:02,000\nEarlier\n\n3\n00:00:04,000 --> 00:00:05,000\nNot included\n", "srt")
	request.Outputs[0].Segments = []ArtifactSegment{
		{SourceStartMS: 6000, SourceEndMS: 7500, OutputStartMS: 0, OutputEndMS: 1500},
		{SourceStartMS: 1000, SourceEndMS: 2000, OutputStartMS: 2000, OutputEndMS: 3000},
		{SourceStartMS: 6000, SourceEndMS: 7000, OutputStartMS: 3000, OutputEndMS: 4000},
	}
	result, err := RetimeSubtitles(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := "1\n00:00:00,000 --> 00:00:01,500\nВопрос\nи ответ\n\n2\n00:00:02,000 --> 00:00:03,000\nEarlier\n\n3\n00:00:03,000 --> 00:00:04,000\nВопрос\nи ответ\n\n"
	got := result.Outputs[0]
	if got.Text != want || got.CueCount != 3 || got.ClipID != "opening" || result.Version != 1 {
		t.Fatalf("unexpected sidecar: %+v", got)
	}
	digest := sha256.Sum256([]byte(want))
	if got.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("sidecar hash differs from actual UTF-8 bytes")
	}
}

func TestRetimeSubtitlesVTTSettingsAndOverlappingCueIdentity(t *testing.T) {
	request := subtitleTestRequest("\ufeffWEBVTT\r\n\r\nNOTE ignored comment\r\n\r\na\r\n00:01.000 --> 00:03.000 align:left position:20%\r\nSame\r\n\r\nb\r\n00:01.000 --> 00:03.000 align:right position:80%\r\nSame\r\n", "vtt")
	result, err := RetimeSubtitles(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := result.Outputs[0]
	if got.CueCount != 2 || !strings.HasPrefix(got.Text, "WEBVTT\n\n") || !strings.Contains(got.Text, "00:00:00.000 --> 00:00:02.000 align:left position:20%") || !strings.Contains(got.Text, "align:right position:80%") {
		t.Fatalf("lost VTT settings or cue: %+v", got)
	}
}

func TestRetimeSubtitlesConversionAndHalfOpenEmptyOutputs(t *testing.T) {
	request := subtitleTestRequest("WEBVTT\n\n00:00.000 --> 00:01.000\nBefore\n\n00:04.000 --> 00:05.000\nAfter\n", "vtt")
	request.OutputFormat = "srt"
	result, err := RetimeSubtitles(context.Background(), request)
	if err != nil || result.Outputs[0].Text != "" || result.Outputs[0].CueCount != 0 {
		t.Fatalf("nonintersecting cues were included: %+v / %v", result, err)
	}
	request.Text = "WEBVTT\n\n00:01.500 --> 00:02.500\n<b>Text</b>\n"
	result, err = RetimeSubtitles(context.Background(), request)
	if err != nil || result.Outputs[0].Text != "1\n00:00:00,500 --> 00:00:01,500\n<b>Text</b>\n\n" {
		t.Fatalf("conversion failed: %+v / %v", result, err)
	}
}

func TestRetimeSubtitlesRejectsUnsupportedAndMalformedText(t *testing.T) {
	cases := []struct{ format, text, code string }{
		{"vtt", "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00.000,MPEGTS:90000\n\n", "subtitle_unsupported"},
		{"vtt", "WEBVTT\n\nSTYLE\n::cue { color: red }\n", "subtitle_unsupported"},
		{"vtt", "WEBVTT\n\nREGION\nid:one\n", "subtitle_unsupported"},
		{"vtt", "WEBVTT\n\n00:01.000 --> 00:02.000\nWord <00:01.500>time\n", "subtitle_unsupported"},
		{"vtt", "WEBVTT\n\n00:01.000 --> 00:02.000 align:banana\nText\n", "subtitle_unsupported"},
		{"srt", "1\n00:00:01,000 --> 00:00:00,000\nSecret text never in errors\n", "invalid_subtitles"},
		{"srt", "1\n00:60:00,000 --> 00:61:00,000\nBad\n", "invalid_subtitles"},
		{"srt", "orphan text", "invalid_subtitles"},
		{"srt", "\x00", "invalid_subtitles"},
		{"srt", "\xff", "invalid_subtitles"},
	}
	for _, tc := range cases {
		t.Run(tc.code+tc.format, func(t *testing.T) {
			_, err := RetimeSubtitles(context.Background(), subtitleTestRequest(tc.text, tc.format))
			var detail *SubtitleRetimeError
			if !errors.As(err, &detail) || detail.Code != tc.code || strings.Contains(err.Error(), "Secret") {
				t.Fatalf("wrong safe diagnostic: %v", err)
			}
		})
	}
}

func TestRetimeSubtitlesMapValidationAndResourceBounds(t *testing.T) {
	base := subtitleTestRequest("1\n00:00:01,000 --> 00:00:03,000\nText\n", "srt")
	for _, segments := range [][]ArtifactSegment{
		{}, {{SourceStartMS: -1, SourceEndMS: 1000, OutputEndMS: 1000}},
		{{SourceStartMS: 0, SourceEndMS: 1000, OutputStartMS: -1, OutputEndMS: 1000}},
		{{SourceStartMS: 0, SourceEndMS: 1000, OutputEndMS: 1000}, {SourceStartMS: 2000, SourceEndMS: 3000, OutputStartMS: 999, OutputEndMS: 2000}},
	} {
		request := base
		request.Outputs = []SubtitleRetimeClip{{ClipID: "test", Segments: segments}}
		if _, err := RetimeSubtitles(context.Background(), request); err == nil {
			t.Fatal("accepted invalid maps")
		}
	}
	request := base
	request.Outputs = append(append([]SubtitleRetimeClip{}, base.Outputs...), base.Outputs[0])
	if _, err := RetimeSubtitles(context.Background(), request); err == nil {
		t.Fatal("accepted duplicate clip IDs")
	}
	request = base
	request.Text = strings.Repeat("x", (512<<10)+1)
	if _, err := RetimeSubtitles(context.Background(), request); err == nil {
		t.Fatal("accepted oversized text")
	}
	request = base
	request.Outputs = make([]SubtitleRetimeClip, 129)
	if _, err := RetimeSubtitles(context.Background(), request); err == nil {
		t.Fatal("accepted too many outputs")
	}
	// The supplied text is small, but repeating the same interval must still
	// respect the aggregate expanded-sidecar budget.
	request = base
	request.Text = "1\n00:00:01,000 --> 00:00:03,000\n" + strings.Repeat("x", 100000) + "\n"
	request.Outputs[0].Segments = make([]ArtifactSegment, 128)
	for i := range request.Outputs[0].Segments {
		request.Outputs[0].Segments[i] = ArtifactSegment{SourceStartMS: 1000, SourceEndMS: 3000, OutputStartMS: int64(i) * 2000, OutputEndMS: int64(i+1) * 2000}
	}
	if _, err := RetimeSubtitles(context.Background(), request); err == nil {
		t.Fatal("accepted expanded output above byte budget")
	}
}

func TestRetimeSubtitlesCancellationAndShortOutputClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := subtitleTestRequest("1\n00:00:01,000 --> 00:00:04,000\nText\n", "srt")
	if _, err := RetimeSubtitles(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	request.Outputs[0].Segments[0].OutputEndMS = 500
	result, err := RetimeSubtitles(context.Background(), request)
	if err != nil || !strings.Contains(result.Outputs[0].Text, "00:00:00,000 --> 00:00:00,500") {
		t.Fatalf("cue exceeded actual output: %+v %v", result, err)
	}
}
