package app

import (
	"context"
	"sort"
)

// Keep at most one small parsed track per operation. Larger source tracks
// retain their existing disk-backed, one-track-at-a-time processing path.
const subtitleIndexCacheBytes = 1 << 20

type subtitleIndex struct {
	cues   []subtitleCue
	maxEnd []int64
}

func newSubtitleIndex(cues []subtitleCue) *subtitleIndex {
	index := &subtitleIndex{cues: cues}
	if len(cues) > subtitleIndexCacheBytes/8 {
		return index
	}
	for i := 1; i < len(cues); i++ {
		if cues[i].StartMS < cues[i-1].StartMS {
			// Preserve the original order even for unsorted valid text tracks.
			return index
		}
	}
	index.maxEnd = make([]int64, len(cues))
	var end int64
	for i, cue := range cues {
		end = max(end, cue.EndMS)
		index.maxEnd[i] = end
	}
	return index
}

func (index *subtitleIndex) intersecting(start, end int64) []subtitleCue {
	if index.maxEnd == nil {
		return index.cues
	}
	first := sort.Search(len(index.cues), func(i int) bool { return index.maxEnd[i] > start })
	last := sort.Search(len(index.cues), func(i int) bool { return index.cues[i].StartMS >= end })
	if first >= last {
		return nil
	}
	return index.cues[first:last]
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
	retained := len(data) + cap(cues)*32 + cap(index.maxEnd)*8 + 128
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
