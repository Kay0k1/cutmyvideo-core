package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// preflightJob inspects prerequisites without creating a job or reserving its
// output. Inspection leases protect local media only for this request; execution
// performs its own admission and validates the source again.
func (s *Server) preflightJob(w http.ResponseWriter, r *http.Request, owner string) {
	var request ExportRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	source, err := s.databaseSource(r.Context(), request.SourceID, owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if source.MediaRemoved {
		sourceMediaError(w, ErrSourceMediaRemoved)
		return
	}
	if err = request.Validate(s.Config, source); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_export", err.Error())
		return
	}
	if err = checkHTTPRenderReference(r.Context(), source, request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_export", err.Error())
		return
	}
	if !s.allow("preflight:"+owner, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limit", "Too many job inspections; wait a minute")
		return
	}
	if source.Path == "" {
		result := preflightRequestResult(source, request)
		// Remote provider metadata is useful for request validation, but it is
		// not a fresh inspection of the media selected when the worker runs.
		addPreflightDeferred(&result, "source_media", "source_media_unverified", "Remote media is resolved and inspected during export")
		addPreflightDeferred(&result, "tracks", "tracks_unverified", "Remote stream selection is checked during export")
		addPreflightDeferred(&result, "video_geometry", "video_geometry_unverified", "Remote display geometry is checked during export")
		addPreflightDeferred(&result, "tool_capabilities", "tool_capabilities_unverified", "Required tool capabilities depend on the remote streams selected during export")
		if request.CutMode == "copy" {
			addPreflightDeferred(&result, "copy_boundaries", "copy_boundaries_deferred", "Copy boundaries are determined from source packets and nearby keyframes during export")
			addPreflightDeferred(&result, "copy_container", "copy_container_unverified", "The configured muxer checks selected stream compatibility during export")
		}
		addPreflightDeferred(&result, "media_decode", "media_decode_deferred", "Media decoding and rendering are checked during export")
		addPreflightDeferred(&result, "output_size", "output_size_deferred", "Final output size is checked during export")
		addPreflightDeferred(&result, "storage", "storage_admission_deferred", "Export queue and storage admission are checked when execution starts")
		addPreflightDeferred(&result, "publication", "publication_deferred", "Output verification and publication are checked during export")
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, result)
		return
	}
	timeout := s.Config.SourceTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	readerCtx, releaseRead, err := s.pinSourceMediaReader(ctx, source)
	if err != nil {
		writePreflightError(w, r, ctx, err)
		return
	}
	ctx = readerCtx
	defer releaseRead()
	// PreflightLocal always inspects afresh. Hold conservative admission for
	// its entire probe/capability-query lifetime instead of borrowing a cached
	// preview inspection whose admission has already ended.
	conservative := source
	conservative.Width, conservative.Height = 0, 0
	started := time.Now()
	var releaseInspection func()
	for {
		releaseInspection, err = s.pinSourceInspection(ctx, conservative, timeout)
		if !errors.Is(err, errSourceServerBusy) || time.Since(started) >= time.Second {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if err != nil && !errors.Is(err, errSourceServerBusy) {
			break
		}
	}
	if err != nil {
		writePreflightError(w, r, ctx, err)
		return
	}
	defer releaseInspection()
	ctx, releaseAudio, err := s.pinExportReplacement(ctx, source, request)
	if err != nil {
		writePreflightError(w, r, ctx, err)
		return
	}
	defer releaseAudio()
	result, err := PreflightLocal(ctx, s.Config, source.Path, request)
	if err != nil {
		writePreflightError(w, r, ctx, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func writePreflightError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
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
		writeError(w, http.StatusGatewayTimeout, "source_timeout", "Job inspection timed out; try again")
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		// Reader renewal uncertainty cancels the protected operation even
		// while the HTTP caller remains connected. Do not certify its result.
		writeError(w, http.StatusServiceUnavailable, "server_busy", "Source inspection was interrupted; try again shortly")
		return
	}
	var tool *exec.Error
	var path *os.PathError
	if errors.As(err, &tool) || errors.As(err, &path) && path.Op == "fork/exec" {
		writeError(w, http.StatusUnprocessableEntity, "tool_unavailable", "A configured media tool is unavailable")
		return
	}
	if problem := problemFromError(err); problem != nil {
		if problem.code == "invalid_argument" {
			writeError(w, http.StatusBadRequest, "invalid_export", problem.message)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, problem.code, problem.message)
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "unsupported_media", "Could not inspect this job; provide supported finite media")
}
