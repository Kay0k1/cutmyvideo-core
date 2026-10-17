package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

const subtitleIndexCacheBytes = subtitles.IndexCacheBytes

type subtitleIndex = subtitles.Index

func sourceSubtitleIndex(ctx context.Context, c Config, source string, stream probeStream) (*subtitleIndex, error) {
	index, err := localmedia.SourceSubtitleIndex(ctx, c, source, stream)
	return index, localMediaError(err)
}
