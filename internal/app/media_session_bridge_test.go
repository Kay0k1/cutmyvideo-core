package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediasession"
)

func TestMediaSessionBridgePreservesLegacyTypesAndCallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("original source bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, session := BeginLocalExport(context.Background(), path)
	defer session.Close()
	if localExportSession(ctx, path, false) != session || localExportSession(ctx, path, true) != nil || localExportSession(ctx, path+"-other", false) != nil {
		t.Fatal("local session context scope changed")
	}
	config := Config{FFprobe: "raw probe identity", FFmpegThreads: 37, MaxOutputBytes: -9, DatabaseURL: "unused-test-value"}
	cause := errors.New("original keyframe error")
	called := false
	_, err := session.nearestKeyframeWithProbe(ctx, config, 1000, 3,
		func(got context.Context, gotConfig Config, gotPath string, remote bool, stream int, from, until int64) ([]copyKeyframe, error) {
			called = true
			if got != ctx || gotConfig != config || gotPath != path || remote || stream != 3 || from != 0 || until != 1001 {
				t.Fatal("legacy keyframe callback arguments changed")
			}
			return nil, cause
		})
	if !called || err != cause {
		t.Fatal("callback cause was wrapped or replaced", err)
	}
	if _, err := session.Session.Inspect(ctx, func(context.Context, string, bool) (mediainfo.Info, error) {
		return mediainfo.Info{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("source changed during the same operation"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = session.unchanged()
	problem, ok := err.(*sourceProblem)
	if !ok || problem.code != "source_changed" || errors.Unwrap(err) != nil {
		t.Fatal("neutral source identity failure lost its legacy concrete type", err)
	}
	state := &LocalExportSession{Session: mediasession.New(t.TempDir())}
	if _, err := state.unchanged(); err != errUnsupportedStream {
		t.Fatal("non-regular source sentinel changed", err)
	}
	wrapped := &localInspectionFailure{cause: context.Canceled}
	if got := mediaInfoError(wrapped); got != wrapped || !errors.Is(got, context.Canceled) {
		t.Fatal("session callback failure lost its original chain", got)
	}
}
