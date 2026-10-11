package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Each joined range retains an independent staged timeline offset. A single
// guard preserves the source job's aggregate transfer and address restrictions.
func stageWorkerSegments(ctx context.Context, c Config, request ExportRequest, guard *networkGuard, streams []platformFormat, playlists []hlsPlaylist, inputs []mediaInput, ranges []Range, dir string, report func(int, int, int)) (segments [][]mediaInput, err error) {
	segments = make([][]mediaInput, len(ranges))
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for i, selected := range ranges {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		segments[i] = append([]mediaInput(nil), inputs...)
		stageDir := filepath.Join(dir, fmt.Sprintf("segment-%d", i))
		for k, stream := range streams {
			if !isHLS(stream) {
				continue
			}
			if k >= len(playlists) || k >= len(segments[i]) {
				return nil, errUnsupportedStream
			}
			if err = os.MkdirAll(stageDir, 0700); err != nil {
				return nil, err
			}
			if report != nil {
				report(i, 0, 1)
			}
			config := c
			config.MaxFetchBytes = inputBudget(c, selected, request)
			var path string
			var offset int64
			path, offset, err = stageHLSProgress(ctx, config, guard, stream, playlists[k], selected, stageDir, k, func(done, total int) {
				if report != nil {
					report(i, done, total)
				}
			})
			if err != nil {
				return nil, err
			}
			segments[i][k] = mediaInput{Path: path, OffsetMS: offset}
		}
	}
	return segments, nil
}
