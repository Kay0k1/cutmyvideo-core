package localmedia

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediasession"
)

type localExportSessionKey struct{}

// LocalExportSession retains the local-operation context and legacy tool/error
// adapters. The bounded media state and lifecycle belong to mediasession.
type LocalExportSession struct{ *mediasession.Session }

func BeginLocalExport(ctx context.Context, path string) (context.Context, *LocalExportSession) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	session := &LocalExportSession{Session: mediasession.New(absolute)}
	return context.WithValue(ctx, localExportSessionKey{}, session), session
}

func localExportSession(ctx context.Context, path string, remote bool) *LocalExportSession {
	if remote {
		return nil
	}
	session, _ := ctx.Value(localExportSessionKey{}).(*LocalExportSession)
	if session == nil {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute != session.Path {
		return nil
	}
	return session
}

func (s *LocalExportSession) unchanged() (os.FileInfo, error) {
	file, err := s.Session.Unchanged()
	return file, mediaInfoError(err)
}

func (s *LocalExportSession) inspect(ctx context.Context, c Config) (probeInfo, error) {
	info, err := s.Session.Inspect(ctx, func(ctx context.Context, path string, remote bool) (mediainfo.Info, error) {
		info, err := probeStreamsUncached(ctx, c, path, remote)
		return mediainfo.Info(info), err
	})
	return probeInfo(info), mediaInfoError(err)
}

func (s *LocalExportSession) nearestKeyframe(ctx context.Context, c Config, start int64, stream int) (copyKeyframe, error) {
	return s.nearestKeyframeWithProbe(ctx, c, start, stream, probeKeyframes)
}

type keyframeProbeFunc func(context.Context, Config, string, bool, int, int64, int64) ([]copyKeyframe, error)

func (s *LocalExportSession) nearestKeyframeWithProbe(ctx context.Context, c Config, start int64, stream int, inspect keyframeProbeFunc) (copyKeyframe, error) {
	frame, err := s.Session.NearestKeyframe(ctx, c.FFprobe, start, stream,
		func(ctx context.Context, path string, remote bool, stream int, from, until int64) ([]copyKeyframe, error) {
			return inspect(ctx, c, path, remote, stream, from, until)
		})
	return frame, mediaInfoError(err)
}

func GetLocalExportSession(ctx context.Context, path string, remote bool) *LocalExportSession {
	return localExportSession(ctx, path, remote)
}
func (s *LocalExportSession) Inspect(ctx context.Context, c Config) (mediainfo.Info, error) {
	p, e := s.inspect(ctx, c)
	return mediainfo.Info(p), e
}
func (s *LocalExportSession) NearestKeyframeWithProbe(ctx context.Context, c Config, start int64, stream int, inspect func(context.Context, Config, string, bool, int, int64, int64) ([]copyKeyframe, error)) (copyKeyframe, error) {
	return s.nearestKeyframeWithProbe(ctx, c, start, stream, inspect)
}
