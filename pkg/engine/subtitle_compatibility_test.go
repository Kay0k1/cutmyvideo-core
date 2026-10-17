package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

var (
	_ *subtitles.RetimeRequest                                                   = (*SubtitleRetimeRequest)(nil)
	_ *subtitles.RetimeClip                                                      = (*SubtitleRetimeClip)(nil)
	_ *subtitles.Sidecar                                                         = (*SubtitleSidecar)(nil)
	_ *subtitles.RetimeResult                                                    = (*SubtitleRetimeResult)(nil)
	_ *subtitles.RetimeError                                                     = (*SubtitleRetimeError)(nil)
	_ *app.SubtitleRetimeRequest                                                 = (*SubtitleRetimeRequest)(nil)
	_ *app.SubtitleRetimeClip                                                    = (*SubtitleRetimeClip)(nil)
	_ *app.SubtitleSidecar                                                       = (*SubtitleSidecar)(nil)
	_ *app.SubtitleRetimeResult                                                  = (*SubtitleRetimeResult)(nil)
	_ *app.SubtitleRetimeError                                                   = (*SubtitleRetimeError)(nil)
	_ func(context.Context, SubtitleRetimeRequest) (SubtitleRetimeResult, error) = RetimeSubtitles
	_ func(*SubtitleRetimeError) string                                          = (*SubtitleRetimeError).Error
)

func TestEngineSubtitleBoundaryPreservesPublicContracts(t *testing.T) {
	t.Run("positional-shapes-and-json", func(t *testing.T) {
		// Local defined shapes preserve positional field checks without using
		// unkeyed literals of types defined in another package.
		type (
			segmentShape Segment
			clipShape    SubtitleRetimeClip
			requestShape SubtitleRetimeRequest
			sidecarShape SubtitleSidecar
			resultShape  SubtitleRetimeResult
			errorShape   SubtitleRetimeError
		)
		segment := Segment(segmentShape{1, "label", 1000, 2000, 0, 1000})
		clip := SubtitleRetimeClip(clipShape{"answer", []Segment{segment}})
		request := SubtitleRetimeRequest(requestShape{1, "srt", "supplied text", "", []SubtitleRetimeClip{clip}})
		for _, test := range []struct {
			value any
			json  string
		}{
			{SubtitleRetimeRequest{}, `{"version":0,"format":"","text":"","outputs":null}`},
			{request, `{"version":1,"format":"srt","text":"supplied text","outputs":[{"clip_id":"answer","segments":[{"index":1,"label":"label","source_start_ms":1000,"source_end_ms":2000,"output_start_ms":0,"output_end_ms":1000}]}]}`},
			{SubtitleSidecar(sidecarShape{"answer", "srt", "text", 1, "hash"}), `{"clip_id":"answer","format":"srt","text":"text","cue_count":1,"sha256":"hash"}`},
			{SubtitleRetimeResult(resultShape{1, []SubtitleSidecar{}}), `{"version":1,"outputs":[]}`},
			{SubtitleRetimeError(errorShape{"invalid_subtitles", "text"}), `{"code":"invalid_subtitles","field":"text"}`},
		} {
			encoded, err := json.Marshal(test.value)
			if err != nil || string(encoded) != test.json {
				t.Fatalf("subtitle public JSON changed: got %s, want %s (%v)", encoded, test.json, err)
			}
		}
		errorType := reflect.TypeOf((*error)(nil)).Elem()
		if reflect.TypeOf(SubtitleRetimeError{}).Implements(errorType) || !reflect.TypeOf((*SubtitleRetimeError)(nil)).Implements(errorType) {
			t.Fatal("subtitle Error method must retain its pointer receiver")
		}
	})

	t.Run("typed-error-without-engine-normalization", func(t *testing.T) {
		result, err := RetimeSubtitles(context.Background(), SubtitleRetimeRequest{})
		var detail *SubtitleRetimeError
		var public *Error
		if !errors.As(err, &detail) || err != detail || detail.Code != "invalid_subtitles" || detail.Field != "version/format" {
			t.Fatalf("subtitle error type or location changed: %T %v", err, err)
		}
		if err.Error() != "invalid_subtitles: version/format" || errors.Unwrap(err) != nil {
			t.Fatal("subtitle error acquired a different diagnostic or cause", err)
		}
		if errors.As(err, &public) || CodeOf(err) != CodeProcessingFailed || app.LocalErrorCode(err) != "" {
			t.Fatal("subtitle error acquired media/engine normalization", err)
		}
		var alias *app.SubtitleRetimeError
		if !errors.As(fmt.Errorf("caller: %w", err), &alias) || alias != detail {
			t.Fatal("caller wrapping lost subtitle alias identity")
		}
		if result.Version != 1 || result.Outputs == nil || len(result.Outputs) != 0 {
			t.Fatal("early validation error changed its initialized empty result", result)
		}
	})

	t.Run("context-errors-remain-direct", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer stop()
		for _, test := range []struct {
			ctx  context.Context
			code ErrorCode
		}{{cancelled, CodeCancelled}, {deadline, CodeTimeout}} {
			result, err := RetimeSubtitles(test.ctx, SubtitleRetimeRequest{})
			var public *Error
			if err != test.ctx.Err() || !errors.Is(err, test.ctx.Err()) || errors.As(err, &public) || CodeOf(err) != test.code || app.LocalErrorCode(err) != "" {
				t.Fatal("context error was wrapped or replaced by request validation", err)
			}
			if result.Version != 1 || result.Outputs == nil || len(result.Outputs) != 0 {
				t.Fatal("early cancellation changed its initialized empty result", result)
			}
		}
	})

	t.Run("late-limit-discards-completed-sidecars", func(t *testing.T) {
		// Valid input stays below 512 KiB. Repeated source ranges produce over
		// 8 MiB across two clips, crossing the limit after the first is complete.
		segments := make([]Segment, 50)
		for i := range segments {
			segments[i] = Segment{SourceEndMS: 1000, OutputStartMS: int64(i) * 1000, OutputEndMS: int64(i+1) * 1000}
		}
		request := SubtitleRetimeRequest{Version: 1, Format: "srt", Text: "1\n00:00:00,000 --> 00:00:01,000\n" + strings.Repeat("x", 100<<10) + "\n",
			Outputs: []SubtitleRetimeClip{{ClipID: "first", Segments: segments}, {ClipID: "second", Segments: segments}}}
		result, err := RetimeSubtitles(context.Background(), request)
		var detail *SubtitleRetimeError
		var public *Error
		if !errors.As(err, &detail) || detail.Code != "subtitle_limit" || detail.Field != "outputs" || errors.As(err, &public) {
			t.Fatal("late output limit lost its typed direct error", err)
		}
		if result.Version != 0 || result.Outputs != nil {
			t.Fatal("late limit exposed partial sidecars instead of the legacy zero result", result.Version, len(result.Outputs))
		}
	})
}
