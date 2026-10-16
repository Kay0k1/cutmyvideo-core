package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CollectionRequest adapts a portable plan to an owner-scoped HTTP source.
// Filesystem inputs are intentionally unavailable through the public API.
type CollectionRequest struct {
	SourceID  string         `json:"source_id"`
	Plan      CollectionPlan `json:"plan"`
	ClipIDs   []string       `json:"clip_ids,omitempty"`
	Rendition string         `json:"rendition,omitempty"`
}

type CollectionDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	ClipID  string `json:"clip_id,omitempty"`
	Field   string `json:"field,omitempty"`
}

type CollectionClipValidation struct {
	ClipID    string          `json:"clip_id"`
	Label     string          `json:"label"`
	Preflight PreflightResult `json:"preflight"`
}

type CollectionValidation struct {
	Version     int                        `json:"version"`
	Valid       bool                       `json:"valid"`
	Rendition   string                     `json:"rendition"`
	RequestedMS int64                      `json:"requested_duration_ms"`
	Clips       []CollectionClipValidation `json:"clips"`
}

type CollectionClipSubmission struct {
	ClipID      string                `json:"clip_id"`
	Label       string                `json:"label"`
	RequestedMS int64                 `json:"requested_duration_ms"`
	Request     ExportRequest         `json:"request"`
	Job         *Job                  `json:"job,omitempty"`
	Error       *CollectionDiagnostic `json:"error,omitempty"`
	Retryable   bool                  `json:"retryable"`
}

// Complete describes queue admission, not media success. Each admitted clip is
// an ordinary job and keeps the existing polling, cancellation and downloads.
type CollectionSubmission struct {
	Version   int                        `json:"version"`
	Rendition string                     `json:"rendition"`
	Complete  bool                       `json:"complete"`
	Clips     []CollectionClipSubmission `json:"clips"`
}

func collectionRendition(value string) string {
	if value == "" {
		return "final"
	}
	return value
}

func collectionPlanDiagnostic(err error) CollectionDiagnostic {
	diagnostic := CollectionDiagnostic{Code: "invalid_collection", Message: err.Error()}
	var field *CollectionFieldError
	if errors.As(err, &field) {
		diagnostic.ClipID, diagnostic.Field = field.ClipID, field.Field
	}
	return diagnostic
}

func writeCollectionPlanError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, struct {
		Error CollectionDiagnostic `json:"error"`
	}{collectionPlanDiagnostic(err)})
}

func (s *Server) collectionRequest(w http.ResponseWriter, r *http.Request, owner string, recoverRetained bool) (CollectionRequest, Source, []PreparedCollectionClip, bool) {
	var request CollectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return request, Source{}, nil, false
	}
	if request.Plan.Input != "" {
		writeCollectionPlanError(w, &CollectionFieldError{Field: "plan.input", Err: errors.New("HTTP collections require source_id; local input paths are unavailable")})
		return request, Source{}, nil, false
	}
	source, err := s.databaseSource(r.Context(), request.SourceID, owner)
	if err != nil {
		lookupError(w, err)
		return request, Source{}, nil, false
	}
	limits, validationSource := s.Config, source
	if recoverRetained {
		// Structural validation derives stable identities before mutable source
		// media and operator budgets. These values NEVER authorize new work.
		// Plan/clip bounds remain 128 each, allowing 128*128 shape intervals.
		limits.MaxRanges, limits.MaxRangeMS, limits.MaxJobMS = 128*128, math.MaxInt64, math.MaxInt64
		validationSource.DurationMS = math.MaxInt64
		validationSource.Path = "collection-identity-only"
	}
	clips, err := NormalizeCollection(request.Plan, CollectionExecution{ClipIDs: request.ClipIDs, Rendition: request.Rendition}, limits, validationSource)
	if err != nil {
		writeCollectionPlanError(w, err)
		return request, source, nil, false
	}
	return request, source, clips, true
}

func (s *Server) validateCollection(w http.ResponseWriter, r *http.Request, owner string) {
	request, source, clips, ok := s.collectionRequest(w, r, owner, false)
	if !ok {
		return
	}
	if source.MediaRemoved {
		sourceMediaError(w, ErrSourceMediaRemoved)
		return
	}
	if !s.allow("preflight:"+owner, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limit", "Too many job inspections; wait a minute")
		return
	}
	reports, failedClip, ctx, release, err := s.inspectCollection(r.Context(), source, clips)
	defer release()
	if err != nil {
		if failedClip != "" && problemFromError(err) != nil {
			problem := problemFromError(err)
			status := http.StatusUnprocessableEntity
			if problem.code == "invalid_argument" {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, struct {
				Error CollectionDiagnostic `json:"error"`
			}{CollectionDiagnostic{Code: problem.code, Message: problem.message, ClipID: failedClip, Field: collectionMediaField(problem.code)}})
		} else {
			writePreflightError(w, r, ctx, err)
		}
		return
	}
	result := CollectionValidation{Version: 1, Valid: true, Rendition: collectionRendition(request.Rendition), Clips: make([]CollectionClipValidation, len(clips))}
	for i, clip := range clips {
		result.RequestedMS += clip.RequestedMS
		result.Clips[i] = CollectionClipValidation{ClipID: clip.ID, Label: clip.Label, Preflight: reports[i]}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

// inspectCollection holds one conservative inspection admission and reader
// lease for the entire batch. Each clip still receives a fresh preflight report.
// Remote validation never resolves a media address or launches a downloader.
func (s *Server) inspectCollection(parent context.Context, source Source, clips []PreparedCollectionClip) ([]PreflightResult, string, context.Context, func(), error) {
	if source.Path == "" {
		reports := make([]PreflightResult, len(clips))
		for i, clip := range clips {
			reports[i] = remoteCollectionPreflight(source, clip.Request)
		}
		return reports, "", parent, func() {}, nil
	}
	timeout := s.Config.SourceTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	readerCtx, releaseReader, err := s.pinSourceMediaReader(ctx, source)
	if err != nil {
		return nil, "", ctx, cancel, err
	}
	ctx = readerCtx
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
		return nil, "", ctx, func() { releaseReader(); cancel() }, err
	}
	release := func() { releaseInspection(); releaseReader(); cancel() }
	reports := make([]PreflightResult, len(clips))
	for i, clip := range clips {
		reports[i], err = PreflightLocal(ctx, s.Config, source.Path, clip.Request)
		if err != nil {
			return nil, clip.ID, ctx, release, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, "", ctx, release, err
	}
	return reports, "", ctx, release, nil
}

func remoteCollectionPreflight(source Source, request ExportRequest) PreflightResult {
	result := preflightRequestResult(source, request)
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
	return result
}

// The caller's key scopes a collection workflow. Effective requests are hashed
// independently, so editing one clip never changes another clip's job identity.
// A new caller key intentionally starts new work even for identical plans.
func collectionJobKey(key string, clip PreparedCollectionClip) string {
	data, _ := json.Marshal(struct {
		Key     string        `json:"key"`
		ClipID  string        `json:"clip_id"`
		Label   string        `json:"label"`
		Request ExportRequest `json:"request"`
	}{key, clip.ID, clip.Label, clip.Request})
	digest := sha256.Sum256(data)
	return "collection:" + hex.EncodeToString(digest[:])
}

func (s *Server) submitCollection(w http.ResponseWriter, r *http.Request, owner string) {
	key := r.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_collection", "Send an Idempotency-Key of 1 to 128 UTF-8 bytes; reuse it for retries and clip edits")
		return
	}
	request, source, clips, ok := s.collectionRequest(w, r, owner, true)
	if !ok {
		return
	}
	result := CollectionSubmission{Version: 1, Rendition: collectionRendition(request.Rendition), Clips: make([]CollectionClipSubmission, len(clips))}
	pending := make([]PreparedCollectionClip, 0, len(clips))
	for i, clip := range clips {
		result.Clips[i] = CollectionClipSubmission{ClipID: clip.ID, Label: clip.Label, RequestedMS: clip.RequestedMS, Request: clip.Request}
		job, retainedProjectJob := scopedProjectExistingJob(r.Context(), clip)
		var err error
		if !retainedProjectJob {
			job, err = apiDatabase(r.Context(), func(ctx context.Context) (Job, error) {
				return s.Store.JobByIdempotencyKey(ctx, owner, collectionJobKey(key, clip))
			})
		}
		if err == nil {
			if err = s.recordScopedProjectJob(r.Context(), owner, clip.ID, job); err != nil {
				lookupError(w, err)
				return
			}
			rewriteProjectJob(r.Context(), &job)
			result.Clips[i].Job = &job
		} else if errors.Is(err, ErrNotFound) {
			pending = append(pending, clip)
		} else {
			diagnostic, _ := collectionAdmissionDiagnostic(err)
			for j := i; j < len(clips); j++ {
				result.Clips[j] = CollectionClipSubmission{ClipID: clips[j].ID, Label: clips[j].Label, RequestedMS: clips[j].RequestedMS, Request: clips[j].Request}
			}
			collectionFailPending(&result, diagnostic, true)
			writeCollectionSubmission(w, result, http.StatusServiceUnavailable)
			return
		}
	}
	if len(pending) == 0 {
		writeCollectionSubmission(w, result, http.StatusAccepted)
		return
	}
	if source.MediaRemoved {
		collectionFailPending(&result, CollectionDiagnostic{Code: "source_removed", Message: "Original source media was removed; retained jobs remain available"}, false)
		writeCollectionSubmission(w, result, http.StatusGone)
		return
	}
	pendingIDs := make([]string, len(pending))
	for i, clip := range pending {
		pendingIDs[i] = clip.ID
	}
	pending, err := NormalizeCollection(request.Plan, CollectionExecution{ClipIDs: pendingIDs, Rendition: request.Rendition}, s.Config, source)
	if err != nil {
		collectionFailPending(&result, collectionPlanDiagnostic(err), false)
		writeCollectionSubmission(w, result, http.StatusBadRequest)
		return
	}
	pendingByID := make(map[string]PreparedCollectionClip, len(pending))
	for _, clip := range pending {
		pendingByID[clip.ID] = clip
	}
	for i := range result.Clips {
		if clip, exists := pendingByID[result.Clips[i].ClipID]; exists {
			result.Clips[i].Request, result.Clips[i].RequestedMS = clip.Request, clip.RequestedMS
		}
	}
	// Inspect pending local jobs before mutation. Retained jobs are recovered
	// without consuming preflight/export allowance or re-inspecting their media.
	if source.Path != "" {
		if !s.allow("preflight:"+owner, 30) {
			collectionFailPending(&result, CollectionDiagnostic{Code: "rate_limit", Message: "Too many job inspections; wait a minute"}, true)
			writeCollectionSubmission(w, result, http.StatusTooManyRequests)
			return
		}
		_, failedClip, ctx, release, err := s.inspectCollection(r.Context(), source, pending)
		if err != nil {
			diagnostic, status := collectionAdmissionDiagnostic(err)
			if sourceTimedOut(ctx, err) {
				diagnostic, status = CollectionDiagnostic{Code: "source_timeout", Message: "Collection inspection timed out; try again"}, http.StatusGatewayTimeout
			}
			diagnostic.ClipID = failedClip
			diagnostic.Field = collectionMediaField(diagnostic.Code)
			release()
			collectionFailPending(&result, diagnostic, status >= 500 || status == http.StatusTooManyRequests)
			writeCollectionSubmission(w, result, status)
			return
		}
		release()
	}
	for i := range result.Clips {
		if result.Clips[i].Job != nil {
			continue
		}
		clip := pendingByID[result.Clips[i].ClipID]
		if !s.allow("export:"+owner, 10) {
			collectionFailPending(&result, CollectionDiagnostic{Code: "rate_limit", Message: "Too many exports; wait a minute"}, true)
			writeCollectionSubmission(w, result, http.StatusTooManyRequests)
			return
		}
		job, err := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) {
			return s.Store.CreateJobLimited(ctx, owner, clip.Request, collectionJobKey(key, clip), 3, s.Config.MaxActiveJobs)
		})
		if err != nil {
			diagnostic, status := collectionAdmissionDiagnostic(err)
			collectionFailPending(&result, diagnostic, status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout)
			writeCollectionSubmission(w, result, status)
			return
		}
		if err = s.recordScopedProjectJob(r.Context(), owner, clip.ID, job); err != nil {
			lookupError(w, err)
			return
		}
		rewriteProjectJob(r.Context(), &job)
		result.Clips[i].Job = &job
	}
	writeCollectionSubmission(w, result, http.StatusAccepted)
}

func collectionFailPending(result *CollectionSubmission, diagnostic CollectionDiagnostic, retryable bool) {
	for i := range result.Clips {
		if result.Clips[i].Job == nil {
			problem := diagnostic
			// Keep attribution of an inspection failure even when it prevented
			// admission of the other clips in the same pending batch.
			if problem.ClipID == "" {
				problem.ClipID = result.Clips[i].ClipID
			}
			result.Clips[i].Error, result.Clips[i].Retryable = &problem, retryable
		}
	}
}

func writeCollectionSubmission(w http.ResponseWriter, result CollectionSubmission, failureStatus int) {
	result.Complete = true
	admitted := 0
	for _, clip := range result.Clips {
		if clip.Job == nil {
			result.Complete = false
		} else {
			admitted++
		}
	}
	status := failureStatus
	if admitted > 0 {
		status = http.StatusAccepted
	}
	if !result.Complete {
		for _, clip := range result.Clips {
			if clip.Retryable && (clip.Error.Code == "rate_limit" || clip.Error.Code == "job_limit") {
				w.Header().Set("Retry-After", "60")
				break
			}
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, result)
}

func collectionMediaField(code string) string {
	switch code {
	case "audio_track_unavailable":
		return "options.audio_track"
	case "crop_invalid", "crop_unsupported":
		return "options.crop"
	case "subtitle_unsupported", "subtitle_mode_unsupported":
		return "options.subtitle_mode"
	default:
		return "request"
	}
}

func collectionAdmissionDiagnostic(err error) (CollectionDiagnostic, int) {
	switch {
	case errors.Is(err, ErrBusy):
		return CollectionDiagnostic{Code: "job_limit", Message: "Wait for active exports to finish, then retry the same collection key"}, http.StatusTooManyRequests
	case errors.Is(err, ErrJobTooLarge):
		return CollectionDiagnostic{Code: "storage_limit", Message: "This clip exceeds the server storage budget; shorten its fragments"}, http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrSourceMediaRemoved):
		return CollectionDiagnostic{Code: "source_removed", Message: "Original source media was removed; retained jobs remain available"}, http.StatusGone
	case errors.Is(err, ErrNotFound):
		return CollectionDiagnostic{Code: "not_found", Message: "The source is unavailable in this session"}, http.StatusNotFound
	case errors.Is(err, errDatabaseUnavailable):
		return CollectionDiagnostic{Code: "database_unavailable", Message: "Database is temporarily unavailable; retry with the same collection key"}, http.StatusServiceUnavailable
	case errors.Is(err, ErrStorageInitializing):
		return CollectionDiagnostic{Code: "storage_initializing", Message: "Storage accounting is being prepared; retry with the same collection key"}, http.StatusServiceUnavailable
	case errors.Is(err, errSourceServerBusy), errors.Is(err, context.Canceled):
		return CollectionDiagnostic{Code: "server_busy", Message: "Source inspection is unavailable; retry with the same collection key"}, http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return CollectionDiagnostic{Code: "request_timeout", Message: "The request timed out; retry with the same collection key"}, http.StatusRequestTimeout
	}
	if problem := problemFromError(err); problem != nil {
		status := http.StatusUnprocessableEntity
		if problem.code == "invalid_argument" {
			status = http.StatusBadRequest
		}
		return CollectionDiagnostic{Code: problem.code, Message: problem.message}, status
	}
	var tool *exec.Error
	var path *os.PathError
	if errors.As(err, &tool) || errors.As(err, &path) && path.Op == "fork/exec" {
		return CollectionDiagnostic{Code: "tool_unavailable", Message: "A configured media tool is unavailable"}, http.StatusUnprocessableEntity
	}
	var inspection *localInspectionFailure
	if errors.As(err, &inspection) || errors.As(err, &path) {
		return CollectionDiagnostic{Code: "unsupported_media", Message: "Could not inspect this clip; provide supported finite media"}, http.StatusUnprocessableEntity
	}
	return CollectionDiagnostic{Code: "server_error", Message: "Submission could not be confirmed; retry with the same collection key"}, http.StatusInternalServerError
}
