package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

type localExportSessionKey struct{}

// LocalExportSession shares inspection and bounded subtitle staging only for
// one original local file within one export operation. Output files, remote
// inputs and independently staged HLS intervals always receive fresh probes.
type LocalExportSession struct {
	mu        sync.Mutex
	path      string
	file      os.FileInfo
	info      probeInfo
	workspace string
	cueDir    string
	cues      map[int]string
}

func BeginLocalExport(ctx context.Context, path string) (context.Context, *LocalExportSession) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	session := &LocalExportSession{path: absolute, cues: make(map[int]string)}
	return context.WithValue(ctx, localExportSessionKey{}, session), session
}

func (s *LocalExportSession) SetWorkspace(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspace = path
}

func (s *LocalExportSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cueDir != "" {
		if err := os.RemoveAll(s.cueDir); err != nil {
			return err
		}
		s.cueDir = ""
		clear(s.cues)
	}
	return nil
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
	if err != nil || absolute != session.path {
		return nil
	}
	return session
}

func (s *LocalExportSession) unchanged() (os.FileInfo, error) {
	file, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if !file.Mode().IsRegular() {
		return nil, errUnsupportedStream
	}
	if s.file != nil && (!os.SameFile(s.file, file) || s.file.Size() != file.Size() || !s.file.ModTime().Equal(file.ModTime())) {
		return nil, &sourceProblem{"source_changed", "The source has changed; open it again before exporting"}
	}
	return file, nil
}

func (s *LocalExportSession) inspect(ctx context.Context, c Config) (probeInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return probeInfo{}, err
	}
	file, err := s.unchanged()
	if err != nil {
		return probeInfo{}, err
	}
	if s.file != nil {
		return s.info, nil
	}
	info, err := probeStreamsUncached(ctx, c, s.path, false)
	if err != nil {
		return info, err
	}
	s.file = file
	if _, err := s.unchanged(); err != nil {
		s.file = nil
		return probeInfo{}, err
	}
	s.info = info
	return info, nil
}
