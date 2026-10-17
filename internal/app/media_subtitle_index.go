package app

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

const subtitleIndexCacheBytes = subtitles.IndexCacheBytes

type subtitleIndex = subtitles.Index

func newSubtitleIndex(cues []subtitleCue) *subtitleIndex {
	return subtitles.NewIndex(cues)
}

func sourceSubtitleIndex(ctx context.Context, c Config, source string, stream probeStream) (*subtitleIndex, error) {
	session := localExportSession(ctx, source, false)
	if session != nil {
		session.mu.Lock()
		if err := ctx.Err(); err != nil {
			session.mu.Unlock()
			return nil, err
		}
		if _, err := session.unchanged(); err != nil {
			session.mu.Unlock()
			return nil, err
		}
		if session.subtitleIndex != nil && session.subtitleTrack == stream.Index && session.subtitleTool == c.FFmpeg {
			index := session.subtitleIndex
			session.mu.Unlock()
			return index, nil
		}
		// A different track must not make all parsed source tracks resident.
		session.subtitleIndex = nil
		session.mu.Unlock()
	}
	data, err := extractTextSubtitles(ctx, c, source, stream)
	if err != nil {
		return nil, err
	}
	cues, err := parseSRT(data)
	if err != nil {
		return nil, &sourceProblem{"subtitle_unsupported", "Source text subtitles contain unsupported timestamps"}
	}
	index := newSubtitleIndex(cues)
	// Text strings can refer to the complete normalized SRT backing string.
	// Account for that string as well as cue structs and the endpoint index.
	// Slice capacity, rather than length, includes append growth slack. Cue
	// structs occupy at most 32 bytes on supported 32/64-bit targets; allow
	// fixed index/object overhead too, even when no endpoint index was built.
	retained := len(data) + cap(cues)*32 + cap(index.MaxEnd)*8 + 128
	for _, cue := range cues {
		retained += len(cue.Text)
	}
	if session != nil && retained <= subtitleIndexCacheBytes {
		session.mu.Lock()
		defer session.mu.Unlock()
		if _, err := session.unchanged(); err != nil {
			return nil, err
		}
		session.subtitleIndex, session.subtitleTrack, session.subtitleTool = index, stream.Index, c.FFmpeg
	}
	return index, nil
}
