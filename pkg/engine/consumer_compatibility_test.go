package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestEngineExternalConsumerPreservesPublicContracts(t *testing.T) {
	root := engineTestRoot(t)
	directory := t.TempDir()
	module := "module example.com/engine-consumer\n\ngo 1.26.9\n\nrequire " + engineModule + " v0.0.0\n\nreplace " + engineModule + " => " + strconv.Quote(filepath.ToSlash(root)) + "\n"
	for name, contents := range map[string]string{"go.mod": module, "consumer_test.go": engineConsumerSource} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// This directory is outside the repository and cannot import its internal
	// packages. Vet stays enabled, and dependencies cannot be downloaded.
	output, err := engineGoCommand(ctx, directory, "test", "-mod=mod", "-count=1", "-timeout=60s", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("external public engine consumer: %v\n%s", err, output)
	}
}

const engineConsumerSource = `package consumer_test

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "reflect"
    "testing"

    "github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

var (
    _ func(*engine.Engine, context.Context, string) (engine.SourceInfo, error) = (*engine.Engine).Inspect
    _ func(*engine.Engine, context.Context, string) (engine.AdvancedSourceInfo, error) = (*engine.Engine).InspectWithTracks
    _ func(*engine.Engine, context.Context, string, string, engine.Range, engine.Options) (engine.Result, error) = (*engine.Engine).Export
    _ func(*engine.Engine, context.Context, string, string, engine.Range, engine.AdvancedOptions) (engine.AdvancedResult, error) = (*engine.Engine).ExportWithOptions
    _ func(*engine.Engine, context.Context, engine.Plan, string) (engine.BatchResult, error) = (*engine.Engine).ExecutePlan
    _ func(*engine.Engine, context.Context, engine.Plan, string) (engine.PreflightResult, error) = (*engine.Engine).PreflightPlan
    _ func(*engine.Engine, context.Context, engine.CollectionPlan, string, engine.CollectionExecution) (engine.CollectionResult, error) = (*engine.Engine).ExecuteCollection
    _ func(*engine.Engine, context.Context, engine.CollectionPlan, string, engine.CollectionExecution) (engine.CollectionPreflightResult, error) = (*engine.Engine).PreflightCollection
    _ func(context.Context, engine.SubtitleRetimeRequest) (engine.SubtitleRetimeResult, error) = engine.RetimeSubtitles
    _ func(*engine.Error) error = (*engine.Error).Unwrap
    _ func(*engine.CollectionFieldError) error = (*engine.CollectionFieldError).Unwrap
)

// Type inference exercises the retained Validate method without importing or
// naming internal configuration/source types, just as an external generic
// consumer can. Their exported shape remains usable through reflection.
func validate[C, S any](t *testing.T, request *engine.PreflightRequest, call func(*engine.PreflightRequest, C, S) error) {
    t.Helper()
    var config C
    var source S
    c := reflect.ValueOf(&config).Elem()
    c.FieldByName("MaxRanges").SetInt(128)
    c.FieldByName("MaxRangeMS").SetInt(1000)
    c.FieldByName("MaxJobMS").SetInt(1000)
    s := reflect.ValueOf(&source).Elem()
    s.FieldByName("DurationMS").SetInt(1000)
    s.FieldByName("Path").SetString("local.mp4")
    if err := call(request, config, source); err != nil {
        t.Fatal("public validation method:", err)
    }
}

func TestPublicConsumer(t *testing.T) {
    // Defined local shapes keep positional arity/order checks meaningful while
    // avoiding vet's restriction on unkeyed literals from another package.
    type rangeShape engine.Range
    type optionsShape engine.Options
    type sourceShape engine.SourceInfo
    type resultShape engine.Result
    interval := engine.Range(rangeShape{0, 1000})
    options := engine.Options(optionsShape{"mp4", "best", "accurate"})
    source := engine.SourceInfo(sourceShape{1000, 640, 360, 640, 360})
    result := engine.Result(resultShape{"out.mp4", 12, 0, 1000})
    _ = map[engine.Range]bool{interval: true}
    _ = map[engine.Options]bool{options: true}
    _ = map[engine.SourceInfo]bool{source: true}
    _ = map[engine.Result]bool{result: true}
    audio := 0
    noAudio := false
    cases := []struct { value any; want string }{
        {interval, "{\"StartMS\":0,\"EndMS\":1000}"},
        {options, "{\"format\":\"mp4\",\"quality\":\"best\",\"cut_mode\":\"accurate\"}"},
        {source, "{\"duration_ms\":1000,\"width\":640,\"height\":360,\"display_width\":640,\"display_height\":360}"},
        {engine.AdvancedSourceInfo{SourceInfo: source, Tracks: []engine.Track{}}, "{\"duration_ms\":1000,\"width\":640,\"height\":360,\"display_width\":640,\"display_height\":360,\"tracks\":[]}"},
        {engine.AdvancedResult{Result: result}, "{\"path\":\"out.mp4\",\"size_bytes\":12,\"actual_start_ms\":0,\"actual_end_ms\":1000,\"track_manifest\":null,\"segments\":null}"},
        {engine.CollectionOptions{AudioTrack: &audio, NoAudio: &noAudio, ClearCrop: true}, "{\"audio_track\":0,\"no_audio\":false,\"clear_crop\":true}"},
        {engine.CollectionResult{Version: 1, Rendition: "final", Outputs: []engine.CollectionOutput{}}, "{\"version\":1,\"rendition\":\"final\",\"outputs\":[]}"},
        {engine.PreflightRequest{}, "{\"source_id\":\"\",\"ranges\":null,\"format\":\"\",\"quality\":\"\",\"cut_mode\":\"\"}"},
        {engine.PreflightCheck{Name: "request", Status: "passed"}, "{\"name\":\"request\",\"status\":\"passed\"}"},
    }
    for _, test := range cases {
        data, err := json.Marshal(test.value)
        if err != nil || string(data) != test.want {
            t.Fatalf("public JSON %T: got %s, want %s (%v)", test.value, data, test.want, err)
        }
    }
    request := engine.PreflightRequest{Ranges: []engine.PreflightRange{{StartMS: 0, EndMS: 1000, Label: " clip "}}, Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "separate", SubtitleMode: "omit", AudioTrack: &audio}
    validate(t, &request, (*engine.PreflightRequest).Validate)
    if request.Ranges[0].Label != "clip" || request.OutputMode != "" || request.SubtitleMode != "" {
        t.Fatal("public request normalization changed", request)
    }

    future := &engine.Error{Code: "future_code", Err: context.Canceled}
    wrapped := fmt.Errorf("consumer: %w", future)
    var public *engine.Error
    if engine.CodeOf(wrapped) != "future_code" || !errors.As(wrapped, &public) || public != future || !errors.Is(wrapped, context.Canceled) || future.Unwrap() != context.Canceled {
        t.Fatal("public error identity or cause changed", wrapped)
    }
    if (&engine.Error{Code: engine.CodeInvalidArgument}).Error() != "invalid_argument" || reflect.TypeOf(engine.Error{}).Implements(reflect.TypeOf((*error)(nil)).Elem()) {
        t.Fatal("public Error pointer method contract changed")
    }
    for _, test := range []struct { err error; code engine.ErrorCode }{
        {errors.Join(engine.ErrPublicationUncertain, context.Canceled), engine.CodePublicationUncertain},
        {errors.Join(engine.ErrSchemaIncompatible, context.Canceled), engine.CodeSchemaIncompatible},
        {errors.Join(engine.ErrStorageInitializing, context.Canceled), engine.CodeCancelled},
        {engine.ErrStorageInitializing, engine.CodeStorageInitializing},
        {engine.ErrOutputExists, engine.CodeOutputExists},
    } {
        if engine.CodeOf(test.err) != test.code { t.Fatalf("public precedence changed: %v", test.err) }
    }
    detail := &engine.CollectionFieldError{ClipID: "clip", Field: "output_path", Err: os.ErrPermission}
    var location *engine.CollectionFieldError
    if !errors.As(fmt.Errorf("consumer: %w", detail), &location) || location != detail || !errors.Is(detail, os.ErrPermission) || engine.CodeOf(detail) != engine.CodePermissionDenied {
        t.Fatal("collection field error lost its public cause", detail)
    }
    instance := engine.New(engine.Config{})
    cancelled, cancel := context.WithCancel(context.Background())
    cancel()
    if _, err := instance.Inspect(cancelled, "unused.mp4"); engine.CodeOf(err) != engine.CodeCancelled || !errors.Is(err, context.Canceled) || !errors.As(err, &public) {
        t.Fatal("public operation error normalization changed", err)
    }
    sidecars, err := engine.RetimeSubtitles(context.Background(), engine.SubtitleRetimeRequest{})
    var subtitle *engine.SubtitleRetimeError
    if !errors.As(err, &subtitle) || errors.As(err, &public) || engine.CodeOf(err) != engine.CodeProcessingFailed || sidecars.Version != 1 || sidecars.Outputs == nil {
        t.Fatal("pure subtitle public error/result changed", sidecars, err)
    }
}
`
