package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func (s *Server) analyzeSourceSilence(w http.ResponseWriter, r *http.Request, owner string) {
	var options *localmedia.SilenceOptions
	if err := decodeJSONLimit(w, r, &options, 8<<10); err != nil {
		return
	}
	if options == nil {
		writeError(w, 400, "invalid_analysis", "Send an analysis options object")
		return
	}
	s.analyzeLocalSource(w, r, owner, options.StartMS, options.EndMS, func(ctx context.Context, path string, end int64) (any, error) {
		copy := *options
		copy.EndMS = end
		return localmedia.AnalyzeSilence(ctx, s.Config, path, copy)
	})
}

func (s *Server) analyzeSourceVideo(w http.ResponseWriter, r *http.Request, owner string) {
	var options *localmedia.VideoAnalysisOptions
	if err := decodeJSONLimit(w, r, &options, 8<<10); err != nil {
		return
	}
	if options == nil {
		writeError(w, 400, "invalid_analysis", "Send an analysis options object")
		return
	}
	s.analyzeLocalSource(w, r, owner, options.StartMS, options.EndMS, func(ctx context.Context, path string, end int64) (any, error) {
		copy := *options
		copy.EndMS = end
		return localmedia.AnalyzeVideo(ctx, s.Config, path, copy)
	})
}

func (s *Server) analyzeSourceLoudness(w http.ResponseWriter, r *http.Request, owner string) {
	var options *localmedia.LoudnessOptions
	if err := decodeJSONLimit(w, r, &options, 8<<10); err != nil {
		return
	}
	if options == nil {
		writeError(w, 400, "invalid_analysis", "Send an analysis options object")
		return
	}
	s.analyzeLocalSource(w, r, owner, options.StartMS, options.EndMS, func(ctx context.Context, path string, end int64) (any, error) {
		copy := *options
		copy.EndMS = end
		return localmedia.AnalyzeLoudness(ctx, s.Config, path, copy)
	})
}

// Analysis retains no cache files or output artifacts. A conservative shared
// decoder admission and renewable reader lease cover the entire operation,
// including probing. Provider addresses never enter this local-only path.
func (s *Server) analyzeLocalSource(w http.ResponseWriter, r *http.Request, owner string, start, end int64, analyze func(context.Context, string, int64) (any, error)) {
	w.Header().Set("Cache-Control", "private, no-store")
	timeout := s.Config.SourceTimeout
	if timeout <= 0 || timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	source, err := s.databaseSource(ctx, r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if source.MediaRemoved {
		sourceMediaError(w, ErrSourceMediaRemoved)
		return
	}
	if source.Path == "" {
		writeError(w, 422, "local_source_required", "Upload a local source before requesting this analysis")
		return
	}
	if end == 0 {
		end = source.DurationMS
	}
	if start < 0 || end <= start || end > source.DurationMS || end-start > localmedia.AnalysisMaxWindowMS {
		writeError(w, 400, "invalid_analysis", "Choose an interval within the source of at most 30 minutes")
		return
	}
	if !s.allow("analysis:"+owner, 6) {
		writeError(w, 429, "rate_limit", "Wait before requesting another media analysis")
		return
	}
	ctx, releaseRead, err := s.pinSourceMediaReader(ctx, source)
	if err != nil {
		writeAnalysisError(w, r, ctx, err)
		return
	}
	defer releaseRead()
	conservative := source
	conservative.Width, conservative.Height = 0, 0
	releaseInspection, err := s.pinSourceInspection(ctx, conservative, timeout)
	if err != nil {
		writeAnalysisError(w, r, ctx, err)
		return
	}
	defer releaseInspection()
	result, err := analyze(ctx, source.Path, end)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		writeAnalysisError(w, r, ctx, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeAnalysisError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	if r.Context().Err() != nil {
		internalError(w, r.Context().Err())
		return
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrSourceMediaRemoved) || errors.Is(err, errSourceServerBusy) {
		sourceMediaError(w, err)
		return
	}
	if errors.Is(err, errDatabaseUnavailable) || errors.Is(err, ErrStorageInitializing) {
		internalError(w, err)
		return
	}
	if sourceTimedOut(ctx, err) {
		writeError(w, 504, "source_timeout", "Media analysis timed out; choose a shorter interval")
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		writeError(w, 503, "server_busy", "Media analysis was interrupted; try again shortly")
		return
	}
	var tool *exec.Error
	var path *os.PathError
	if errors.As(err, &tool) || errors.As(err, &path) && path.Op == "fork/exec" {
		writeError(w, 422, "tool_unavailable", "A configured media tool is unavailable")
		return
	}
	if problem := problemFromError(err); problem != nil {
		if problem.code == "invalid_argument" {
			writeError(w, 400, "invalid_analysis", problem.message)
			return
		}
		writeError(w, 422, problem.code, problem.message)
		return
	}
	writeError(w, 422, "unsupported_media", "Could not analyze this source; provide supported finite media")
}
