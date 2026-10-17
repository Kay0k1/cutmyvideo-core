package app

import (
	"context"
	"os"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediasession"
)

type localExportSessionKey struct{}

// LocalExportSession retains the local-operation context and legacy tool/error
// adapters. The bounded media state and lifecycle belong to mediasession.
type LocalExportSession struct{ *mediasession.Session }

const sessionKeyframeWindows = mediasession.KeyframeWindows
const sessionKeyframeEntries = mediasession.KeyframeEntries

type keyframeWindow = mediasession.KeyframeWindow

func BeginLocalExport(ctx context.Context, path string) (context.Context, *LocalExportSession) {
	ctx, state := localmedia.BeginLocalExport(ctx, path)
	session := &LocalExportSession{Session: state.Session}
	return context.WithValue(ctx, localExportSessionKey{}, session), session
}

func localExportSession(ctx context.Context, path string, remote bool) *LocalExportSession {
	state := localmedia.GetLocalExportSession(ctx, path, remote)
	if state == nil {
		return nil
	}
	if session, _ := ctx.Value(localExportSessionKey{}).(*LocalExportSession); session != nil && session.Session == state.Session {
		return session
	}
	return &LocalExportSession{Session: state.Session}
}

func (s *LocalExportSession) unchanged() (os.FileInfo, error) {
	file, err := s.Session.Unchanged()
	return file, mediaInfoError(err)
}

func (s *LocalExportSession) inspect(ctx context.Context, c Config) (probeInfo, error) {
	p, e := (&localmedia.LocalExportSession{Session: s.Session}).Inspect(ctx, c)
	return probeInfo(p), localMediaError(e)
}

func (s *LocalExportSession) nearestKeyframe(ctx context.Context, c Config, start int64, stream int) (copyKeyframe, error) {
	return s.nearestKeyframeWithProbe(ctx, c, start, stream, probeKeyframes)
}

type keyframeProbeFunc func(context.Context, Config, string, bool, int, int64, int64) ([]copyKeyframe, error)

func (s *LocalExportSession) nearestKeyframeWithProbe(ctx context.Context, c Config, start int64, stream int, inspect keyframeProbeFunc) (copyKeyframe, error) {
	frame, e := (&localmedia.LocalExportSession{Session: s.Session}).NearestKeyframeWithProbe(ctx, c, start, stream, inspect)
	return frame, localMediaError(e)
}
