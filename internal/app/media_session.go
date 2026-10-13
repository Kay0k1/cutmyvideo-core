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
	mu            sync.Mutex
	path          string
	file          os.FileInfo
	info          probeInfo
	workspace     string
	cueDir        string
	cues          map[int]string
	rangeStarts   []int64
	keyframes     []keyframeWindow
	keyframeFile  os.FileInfo
	subtitleIndex *subtitleIndex
	subtitleTrack int
	subtitleTool  string
}

const sessionKeyframeWindows = 16
const sessionKeyframeEntries = 4096

type keyframeWindow struct {
	from, until int64
	stream      int
	tool        string
	frames      []copyKeyframe
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

// SetRanges enables bounded lookahead for the original local file only. The
// operation never reads a whole recording just to prepare a keyframe index.
func (s *LocalExportSession) SetRanges(ranges []Range) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rangeStarts = make([]int64, 0, min(len(ranges), 128))
	for _, r := range ranges[:min(len(ranges), 128)] {
		if r.StartMS >= 0 {
			s.rangeStarts = append(s.rangeStarts, r.StartMS)
		}
	}
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
	s.keyframes = nil
	s.keyframeFile = nil
	s.rangeStarts = nil
	s.subtitleIndex = nil
	return nil
}

func (s *LocalExportSession) nearestKeyframe(ctx context.Context, c Config, start int64, stream int) (copyKeyframe, error) {
	return s.nearestKeyframeWithProbe(ctx, c, start, stream, probeKeyframes)
}

type keyframeProbeFunc func(context.Context, Config, string, bool, int, int64, int64) ([]copyKeyframe, error)

func (s *LocalExportSession) nearestKeyframeWithProbe(ctx context.Context, c Config, start int64, stream int, inspect keyframeProbeFunc) (copyKeyframe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return copyKeyframe{}, err
	}
	file, err := s.unchanged()
	if err != nil {
		return copyKeyframe{}, err
	}
	if s.keyframeFile != nil && (!os.SameFile(s.keyframeFile, file) || s.keyframeFile.Size() != file.Size() || !s.keyframeFile.ModTime().Equal(file.ModTime())) {
		return copyKeyframe{}, &sourceProblem{"source_changed", "The source has changed; open it again before exporting"}
	}
	from, until := keyframeProbeBounds(start)
	for _, cached := range s.keyframes {
		if cached.stream == stream && cached.tool == c.FFprobe && cached.from <= from && cached.until >= until {
			return selectCopyKeyframe(cached.frames, start)
		}
	}
	probeUntil := until
	for _, other := range s.rangeStarts {
		if other >= start && other-start <= 15000 {
			_, end := keyframeProbeBounds(other)
			probeUntil = max(probeUntil, end)
		}
	}
	frames, err := inspect(ctx, c, s.path, false, stream, from, probeUntil)
	if err != nil && probeUntil != until && ctx.Err() == nil {
		// Optional lookahead must not reject a cut that its original bounded
		// read supports (for example a damaged packet later in the source).
		probeUntil = until
		frames, err = inspect(ctx, c, s.path, false, stream, from, until)
	}
	if err != nil {
		return copyKeyframe{}, err
	}
	after, err := s.unchanged()
	if err != nil {
		return copyKeyframe{}, err
	}
	if !os.SameFile(file, after) || file.Size() != after.Size() || !file.ModTime().Equal(after.ModTime()) {
		return copyKeyframe{}, &sourceProblem{"source_changed", "The source has changed; open it again before exporting"}
	}
	if len(frames) <= sessionKeyframeEntries {
		s.keyframeFile = after
		var kept []keyframeWindow
		entries := len(frames)
		for _, cached := range s.keyframes {
			if cached.stream == stream && cached.tool == c.FFprobe && from <= cached.from && probeUntil >= cached.until {
				continue
			}
			if len(kept) < sessionKeyframeWindows-1 && entries+len(cached.frames) <= sessionKeyframeEntries {
				kept = append(kept, cached)
				entries += len(cached.frames)
			}
		}
		s.keyframes = append([]keyframeWindow{{from: from, until: probeUntil, stream: stream, tool: c.FFprobe, frames: frames}}, kept...)
	}
	return selectCopyKeyframe(frames, start)
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
