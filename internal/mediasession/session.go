package mediasession

import (
	"context"
	"os"
	"sync"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

// Session owns bounded metadata and subtitle staging for one original local
// file in one export. Callers hold Mu when seeding or reading shared state.
type Session struct {
	Mu            sync.Mutex
	Path          string
	File          os.FileInfo
	Info          mediainfo.Info
	Workspace     string
	CueDir        string
	Cues          map[int]string
	RangeStarts   []int64
	Keyframes     []KeyframeWindow
	KeyframeFile  os.FileInfo
	SubtitleIndex *subtitles.Index
	SubtitleTrack int
	SubtitleTool  string
}

const KeyframeWindows = 16
const KeyframeEntries = 4096

type KeyframeWindow struct {
	From, Until int64
	Stream      int
	Tool        string
	Frames      []mediaexport.Keyframe
}

func (s *Session) SetWorkspace(path string) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Workspace = path
}

// SetRanges enables bounded lookahead for the original local file only. The
// operation never reads a whole recording just to prepare a keyframe index.
func (s *Session) SetRanges(ranges []edit.Range) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.RangeStarts = make([]int64, 0, min(len(ranges), 128))
	for _, r := range ranges[:min(len(ranges), 128)] {
		if r.StartMS >= 0 {
			s.RangeStarts = append(s.RangeStarts, r.StartMS)
		}
	}
}

func (s *Session) Close() error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if s.CueDir != "" {
		if err := os.RemoveAll(s.CueDir); err != nil {
			return err
		}
		s.CueDir = ""
		clear(s.Cues)
	}
	s.Keyframes = nil
	s.KeyframeFile = nil
	s.RangeStarts = nil
	s.SubtitleIndex = nil
	return nil
}

type KeyframeProbeFunc func(context.Context, string, bool, int, int64, int64) ([]mediaexport.Keyframe, error)

func (s *Session) NearestKeyframe(ctx context.Context, tool string, start int64, stream int, inspect KeyframeProbeFunc) (mediaexport.Keyframe, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := ctx.Err(); err != nil {
		return mediaexport.Keyframe{}, err
	}
	file, err := s.Unchanged()
	if err != nil {
		return mediaexport.Keyframe{}, err
	}
	if s.KeyframeFile != nil && (!os.SameFile(s.KeyframeFile, file) || s.KeyframeFile.Size() != file.Size() || !s.KeyframeFile.ModTime().Equal(file.ModTime())) {
		return mediaexport.Keyframe{}, &mediainfo.Problem{Code: "source_changed", Message: "The source has changed; open it again before exporting"}
	}
	from, until := mediaexport.KeyframeProbeBounds(start)
	for _, cached := range s.Keyframes {
		if cached.Stream == stream && cached.Tool == tool && cached.From <= from && cached.Until >= until {
			return mediaexport.SelectCopyKeyframe(cached.Frames, start)
		}
	}
	probeUntil := until
	for _, other := range s.RangeStarts {
		if other >= start && other-start <= 15000 {
			_, end := mediaexport.KeyframeProbeBounds(other)
			probeUntil = max(probeUntil, end)
		}
	}
	frames, err := inspect(ctx, s.Path, false, stream, from, probeUntil)
	if err != nil && probeUntil != until && ctx.Err() == nil {
		// Optional lookahead must not reject a cut that its original bounded
		// read supports (for example a damaged packet later in the source).
		probeUntil = until
		frames, err = inspect(ctx, s.Path, false, stream, from, until)
	}
	if err != nil {
		return mediaexport.Keyframe{}, err
	}
	after, err := s.Unchanged()
	if err != nil {
		return mediaexport.Keyframe{}, err
	}
	if !os.SameFile(file, after) || file.Size() != after.Size() || !file.ModTime().Equal(after.ModTime()) {
		return mediaexport.Keyframe{}, &mediainfo.Problem{Code: "source_changed", Message: "The source has changed; open it again before exporting"}
	}
	if len(frames) <= KeyframeEntries {
		s.KeyframeFile = after
		var kept []KeyframeWindow
		entries := len(frames)
		for _, cached := range s.Keyframes {
			if cached.Stream == stream && cached.Tool == tool && from <= cached.From && probeUntil >= cached.Until {
				continue
			}
			if len(kept) < KeyframeWindows-1 && entries+len(cached.Frames) <= KeyframeEntries {
				kept = append(kept, cached)
				entries += len(cached.Frames)
			}
		}
		s.Keyframes = append([]KeyframeWindow{{From: from, Until: probeUntil, Stream: stream, Tool: tool, Frames: frames}}, kept...)
	}
	return mediaexport.SelectCopyKeyframe(frames, start)
}

// Unchanged checks the original file. Callers hold Mu when the session is shared.
func (s *Session) Unchanged() (os.FileInfo, error) {
	file, err := os.Stat(s.Path)
	if err != nil {
		return nil, err
	}
	if !file.Mode().IsRegular() {
		return nil, mediainfo.ErrUnsupported
	}
	if s.File != nil && (!os.SameFile(s.File, file) || s.File.Size() != file.Size() || !s.File.ModTime().Equal(file.ModTime())) {
		return nil, &mediainfo.Problem{Code: "source_changed", Message: "The source has changed; open it again before exporting"}
	}
	return file, nil
}

func (s *Session) Inspect(ctx context.Context, inspect ProbeFunc) (mediainfo.Info, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := ctx.Err(); err != nil {
		return mediainfo.Info{}, err
	}
	file, err := s.Unchanged()
	if err != nil {
		return mediainfo.Info{}, err
	}
	if s.File != nil {
		return s.Info, nil
	}
	info, err := inspect(ctx, s.Path, false)
	if err != nil {
		return info, err
	}
	s.File = file
	if _, err := s.Unchanged(); err != nil {
		s.File = nil
		return mediainfo.Info{}, err
	}
	s.Info = info
	return info, nil
}

type ProbeFunc func(context.Context, string, bool) (mediainfo.Info, error)

// New creates operation-local state. The caller supplies its normalized path.
func New(path string) *Session {
	return &Session{Path: path, Cues: make(map[int]string)}
}
