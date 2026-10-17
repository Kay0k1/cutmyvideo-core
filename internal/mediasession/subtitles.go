package mediasession

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

func (session *Session) ExtractText(ctx context.Context, track int, extract func() ([]byte, error)) ([]byte, error) {
	if session != nil {
		session.Mu.Lock()
		defer session.Mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := session.Unchanged(); err != nil {
			return nil, err
		}
		if path := session.Cues[track]; path != "" {
			return os.ReadFile(path)
		}
		data, err := extract()
		if err != nil {
			return nil, err
		}
		if _, err := session.Unchanged(); err != nil {
			return nil, err
		}
		// Cache on disk inside the reserved workspace. Only one bounded track
		// is decoded into Go memory at a time, even for a many-track source.
		if session.Workspace != "" {
			if session.CueDir == "" {
				session.CueDir, err = os.MkdirTemp(session.Workspace, ".source-subtitles-")
				if err != nil {
					return nil, err
				}
			}
			path := filepath.Join(session.CueDir, fmt.Sprintf("track-%d.srt", track))
			if err := os.WriteFile(path, data, 0600); err != nil {
				return nil, err
			}
			session.Cues[track] = path
		}
		return data, nil
	}
	return extract()
}

func (session *Session) SourceSubtitleIndex(ctx context.Context, track int, tool string, extract func() ([]byte, error)) (*subtitles.Index, error) {
	if session != nil {
		session.Mu.Lock()
		if err := ctx.Err(); err != nil {
			session.Mu.Unlock()
			return nil, err
		}
		if _, err := session.Unchanged(); err != nil {
			session.Mu.Unlock()
			return nil, err
		}
		if session.SubtitleIndex != nil && session.SubtitleTrack == track && session.SubtitleTool == tool {
			index := session.SubtitleIndex
			session.Mu.Unlock()
			return index, nil
		}
		// A different track must not make all parsed source tracks resident.
		session.SubtitleIndex = nil
		session.Mu.Unlock()
	}
	data, err := extract()
	if err != nil {
		return nil, err
	}
	cues, err := subtitles.ParseSourceSRT(data)
	if err != nil {
		return nil, &mediainfo.Problem{Code: "subtitle_unsupported", Message: "Source text subtitles contain unsupported timestamps"}
	}
	index := subtitles.NewIndex(cues)
	// Text strings can refer to the complete normalized SRT backing string.
	// Account for that string as well as cue structs and the endpoint index.
	// Slice capacity, rather than length, includes append growth slack. Cue
	// structs occupy at most 32 bytes on supported 32/64-bit targets; allow
	// fixed index/object overhead too, even when no endpoint index was built.
	retained := len(data) + cap(cues)*32 + cap(index.MaxEnd)*8 + 128
	for _, cue := range cues {
		retained += len(cue.Text)
	}
	if session != nil && retained <= subtitles.IndexCacheBytes {
		session.Mu.Lock()
		defer session.Mu.Unlock()
		if _, err := session.Unchanged(); err != nil {
			return nil, err
		}
		session.SubtitleIndex, session.SubtitleTrack, session.SubtitleTool = index, track, tool
	}
	return index, nil
}
