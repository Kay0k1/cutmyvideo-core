package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

func (s *Server) source(w http.ResponseWriter, r *http.Request, owner string) {
	v, err := s.databaseSource(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) sourceMedia(w http.ResponseWriter, r *http.Request, owner string) {
	v, err := s.databaseSource(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if v.MediaRemoved {
		sourceMediaError(w, ErrSourceMediaRemoved)
		return
	}
	if v.Path == "" {
		writeError(w, 404, "preview_unavailable", "A native preview is not available for this source")
		return
	}
	ctx, release, err := s.pinSourceMediaReader(r.Context(), v)
	if err != nil {
		sourceMediaError(w, err)
		return
	}
	defer release()
	serveFile(w, r.WithContext(ctx), v.Path, v.Title, false)
}

func (s *Server) sourceTracks(w http.ResponseWriter, r *http.Request, owner string) {
	source, err := s.databaseSource(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if source.MediaRemoved {
		sourceMediaError(w, ErrSourceMediaRemoved)
		return
	}
	if source.Path == "" {
		writeError(w, http.StatusUnprocessableEntity, "tracks_unavailable", "Source track inspection requires an uploaded file; upload the video to choose an audio track")
		return
	}
	if !s.allow("tracks:"+owner, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limit", "Too many track inspections; wait a minute")
		return
	}
	timeout := s.Config.SourceTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	ctx, source, release, err := s.prepareLocalSource(ctx, source, timeout)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrSourceMediaRemoved) {
			sourceMediaError(w, err)
		} else if errors.Is(err, errSourceServerBusy) || errors.Is(err, errDatabaseUnavailable) || sourceTimedOut(ctx, err) {
			writeSourceAdmissionError(w, ctx, err)
		} else {
			writeError(w, http.StatusUnprocessableEntity, "unsupported_media", "Could not inspect source tracks; upload a supported media file")
		}
		return
	}
	defer release()
	inspected, err := InspectLocal(ctx, s.Config, source.Path)
	if err != nil {
		if r.Context().Err() != nil {
			internalError(w, r.Context().Err())
			return
		}
		if ctx.Err() != nil {
			writeError(w, http.StatusGatewayTimeout, "source_timeout", "Source track inspection timed out; try again")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "unsupported_media", "Could not inspect source tracks; upload a supported media file")
		return
	}
	if inspected.Tracks == nil {
		inspected.Tracks = []Track{}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, struct {
		Tracks        []Track `json:"tracks"`
		DisplayWidth  int     `json:"display_width,omitempty"`
		DisplayHeight int     `json:"display_height,omitempty"`
	}{inspected.Tracks, inspected.DisplayWidth, inspected.DisplayHeight})
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request, owner string) {
	paths, err := apiDatabase(r.Context(), func(ctx context.Context) ([]string, error) {
		return s.Store.DeleteSource(ctx, r.PathValue("id"), owner)
	})
	if errors.Is(err, ErrSourceInUse) {
		writeError(w, 409, "source_in_use", "Cancel or finish active exports before deleting this source")
		return
	}
	if err != nil {
		lookupError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), workerDatabaseTimeout)
	defer cancel()
	if err = s.Store.DrainStorageDeletes(ctx, s.Config, paths); err != nil {
		slog.Warn("source files awaiting deletion", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request, owner string) {
	sources, err := apiDatabase(r.Context(), func(ctx context.Context) ([]Source, error) {
		return s.Store.Sources(ctx, owner)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sources": sources})
}
