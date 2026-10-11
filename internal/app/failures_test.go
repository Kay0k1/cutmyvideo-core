package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExportProblemsAreFixedPublicDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code string
	}{
		{errNoAudio, "audio_missing"}, {errOutputLimit, "output_limit"}, {errPlatformAccess, "platform_access_required"},
		{context.DeadlineExceeded, "job_timeout"},
		{&mediaProcessFailure{cause: errors.New("https://cdn.example/private?token=SECRET"), category: "network_timeout"}, "media_network_timeout"},
		{&mediaProcessFailure{cause: errors.New("SECRET"), category: "upstream_denied"}, "media_upstream_denied"},
		{&mediaProcessFailure{cause: errors.New("SECRET"), category: "upstream_unavailable"}, "media_upstream_unavailable"},
		{&mediaProcessFailure{cause: errors.New("SECRET"), category: "network_reset"}, "media_network_reset"},
		{&mediaProcessFailure{cause: errors.New("SECRET"), category: "missing_audio"}, "audio_missing"},
		{&mediaProcessFailure{cause: errors.New("SECRET"), category: "copy_incompatible"}, "copy_incompatible"},
		{&mediaProcessFailure{cause: errors.New("SECRET"), category: "storage_full"}, "storage_full"},
		{&mediaProcessFailure{cause: errors.New("SECRET"), category: "unsupported_media"}, "unsupported_stream"},
		{errors.New("https://cdn.example/private?token=SECRET"), "media_processing_failed"},
	} {
		problem := exportProblem(tt.err)
		if problem.code != tt.code || strings.Contains(problem.message, "SECRET") || strings.Contains(problem.message, "cdn.example") {
			t.Fatalf("unsafe or wrong public diagnostic: %+v", problem)
		}
	}
}

func TestJobItemErrorCodeIsAdditiveAndOmittedWhenAbsent(t *testing.T) {
	var legacy JobItem
	if err := json.Unmarshal([]byte(`{"id":"old","status":"failed","message":"old fixed message"}`), &legacy); err != nil || legacy.ErrorCode != "" {
		t.Fatal("legacy item became incompatible", err)
	}
	data, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(data), "error_code") {
		t.Fatal("absent diagnostic was emitted", err)
	}
	legacy.ErrorCode = "audio_missing"
	data, err = json.Marshal(legacy)
	if err != nil || !strings.Contains(string(data), `"error_code":"audio_missing"`) {
		t.Fatal("safe diagnostic was omitted", err)
	}
}

func TestExperimentalContainerCopyDiagnosticPreservesCause(t *testing.T) {
	cause := errors.New("private cause TOKEN")
	stderr := "[mp4] flac in MP4 support is experimental, add -strict -2 if you want to enable it"
	failure := &mediaProcessFailure{cause: cause, category: mediaFailureCategory(stderr)}
	result := copyExportDiagnostic(failure, "mp4")
	if LocalErrorCode(result) != "copy_incompatible" || !strings.Contains(result.Error(), "MKV") || strings.Contains(result.Error(), "TOKEN") {
		t.Fatalf("unsafe or unhelpful copy diagnostic: %v", result)
	}
	if !errors.Is(result, cause) {
		t.Fatal("copy diagnostic discarded its process cause")
	}
	if problem := exportProblem(result); problem.code != "copy_incompatible" || strings.Contains(problem.message, "TOKEN") {
		t.Fatalf("unsafe public copy diagnostic: %+v", problem)
	}
}
