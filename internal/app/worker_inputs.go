package app

import (
	"context"
	"errors"
	"math"
)

func platformInputs(ctx context.Context, c Config, store *Store, source Source, guard *networkGuard, request ExportRequest, forceFresh bool) ([]mediaInput, []platformFormat, []hlsPlaylist, bool, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, c.SourceTimeout)
	defer cancel()
	var info platformInfo
	var cached bool
	var err error
	if forceFresh {
		info, err = store.ForceFreshPlatformMetadata(resolveCtx, c, source, guard)
	} else {
		info, cached, err = store.ResolvePlatformMetadata(resolveCtx, c, source, guard)
	}
	if err != nil {
		return nil, nil, nil, cached, err
	}
	if info.ID != source.ProviderID || math.Abs(info.Duration*1000-float64(source.DurationMS)) > 1000 {
		return nil, nil, nil, cached, &sourceProblem{"source_changed", "The source has changed; open it again before exporting"}
	}
	streams, err := pickStreams(info, request.Quality, request.Format)
	if err != nil {
		return nil, nil, nil, cached, err
	}
	inputs := make([]mediaInput, len(streams))
	playlists := make([]hlsPlaylist, len(streams))
	for i, f := range streams {
		if isHLS(f) {
			playlists[i], err = loadHLS(resolveCtx, guard, f, request.Quality, 0)
			if err != nil {
				return nil, streams, playlists, cached, err
			}
			if math.Abs(float64(playlists[i].DurationMS-source.DurationMS)) > 2000 {
				return nil, streams, playlists, cached, &sourceProblem{"source_timeline_changed", "The recording is incomplete or its timeline changed; open a completed recording instead"}
			}
		} else {
			u, e := guard.RelayWithHeaders(f.URL, f.Headers)
			if e != nil {
				return nil, streams, playlists, cached, errUnsupportedStream
			}
			inputs[i] = mediaInput{Path: u, Remote: true}
		}
	}
	return inputs, streams, playlists, cached, nil
}

// Only a denied upstream address taken from the private short-lived cache can
// trigger one re-resolution. Encoding, limits, cancellation and fresh denials
// remain terminal, and the same guard transfer budget covers both attempts.
func cachedAddressDenied(err error) bool {
	var failure *mediaProcessFailure
	if errors.As(err, &failure) && failure.category == "upstream_denied" {
		return true
	}
	var problem *sourceProblem
	return errors.As(err, &problem) && problem.code == "media_upstream_denied"
}
