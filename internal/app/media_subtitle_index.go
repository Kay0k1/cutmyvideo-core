package app

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediasession"
	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

const subtitleIndexCacheBytes = subtitles.IndexCacheBytes

type subtitleIndex = subtitles.Index

func newSubtitleIndex(cues []subtitleCue) *subtitleIndex {
	return subtitles.NewIndex(cues)
}

func sourceSubtitleIndex(ctx context.Context, c Config, source string, stream probeStream) (*subtitleIndex, error) {
	var state *mediasession.Session
	if session := localExportSession(ctx, source, false); session != nil {
		state = session.Session
	}
	index, err := state.SourceSubtitleIndex(ctx, stream.Index, c.FFmpeg, func() ([]byte, error) {
		return extractTextSubtitles(ctx, c, source, stream)
	})
	return index, mediaInfoError(err)
}
