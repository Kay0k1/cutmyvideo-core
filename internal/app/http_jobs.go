package app

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func (s *Server) createJob(w http.ResponseWriter, r *http.Request, owner string) {
	var request ExportRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		writeError(w, 400, "invalid_export", "Idempotency key is too long")
		return
	}
	if key != "" {
		// Recover accepted work before mutable source/admission checks. The
		// retained owner/key pair keeps its established return-original meaning.
		j, err := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) {
			return s.Store.JobByIdempotencyKey(ctx, owner, key)
		})
		if err == nil {
			writeJSON(w, 202, j)
			return
		}
		if !errors.Is(err, ErrNotFound) {
			lookupError(w, err)
			return
		}
	}
	v, err := s.databaseSource(r.Context(), request.SourceID, owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if v.MediaRemoved {
		sourceMediaError(w, ErrSourceMediaRemoved)
		return
	}
	if err = request.Validate(s.Config, v); err != nil {
		writeError(w, 400, "invalid_export", err.Error())
		return
	}
	if err = checkHTTPRenderReference(r.Context(), v, request); err != nil {
		writeError(w, 400, "invalid_export", err.Error())
		return
	}
	if edit.HasRenderOptions(edit.ExportRequest(request)) && v.Path != "" {
		_, _, ctx, release, inspectErr := s.inspectCollection(r.Context(), v, []PreparedCollectionClip{{Request: request}})
		defer release()
		if inspectErr != nil {
			writePreflightError(w, r, ctx, inspectErr)
			return
		}
	} else if v.Path != "" && (request.AudioTrack != nil || request.SubtitleMode == "preserve" || request.Crop != nil) {
		timeout := s.Config.SourceTimeout
		if timeout <= 0 {
			timeout = 2 * time.Minute
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		ctx, _, release, pinErr := s.prepareLocalSource(ctx, v, timeout)
		if pinErr != nil {
			cancel()
			if r.Context().Err() != nil {
				return
			}
			if errors.Is(pinErr, ErrNotFound) || errors.Is(pinErr, ErrSourceMediaRemoved) {
				sourceMediaError(w, pinErr)
			} else if errors.Is(pinErr, errSourceServerBusy) || errors.Is(pinErr, errDatabaseUnavailable) || sourceTimedOut(ctx, pinErr) {
				writeSourceAdmissionError(w, r.Context(), pinErr)
			} else {
				problem := exportProblem(pinErr)
				writeError(w, http.StatusUnprocessableEntity, problem.code, problem.message)
			}
			return
		}
		_, inspectionErr := InspectExportLocal(ctx, s.Config, v.Path, request)
		cancel()
		release()
		if inspectionErr != nil {
			if r.Context().Err() != nil {
				internalError(w, r.Context().Err())
				return
			}
			problem := exportProblem(inspectionErr)
			writeError(w, http.StatusUnprocessableEntity, problem.code, problem.message)
			return
		}
	}
	if !s.allow("export:"+owner, 10) {
		writeError(w, 429, "rate_limit", "Too many exports; wait a minute")
		return
	}
	j, err := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) {
		return s.Store.CreateJobLimited(ctx, owner, request, key, 3, s.Config.MaxActiveJobs)
	})
	if errors.Is(err, ErrJobTooLarge) {
		writeError(w, 413, "storage_limit", "This export exceeds the server storage budget; select fewer fragments")
		return
	}
	if errors.Is(err, ErrBusy) {
		writeError(w, 429, "job_limit", "Wait for an active export before creating another")
		return
	}
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 202, j)
}

func (s *Server) job(w http.ResponseWriter, r *http.Request, owner string) {
	j, err := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) {
		return s.Store.Job(ctx, r.PathValue("id"), owner)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	rewriteProjectJob(r.Context(), &j)
	writeJSON(w, 200, j)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, owner string) {
	if err := apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		return s.Store.Cancel(ctx, r.PathValue("id"), owner)
	}); err != nil {
		lookupError(w, err)
		return
	}
	s.job(w, r, owner)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, owner string) {
	type location struct {
		path, name string
		draft      bool
	}
	result, err := apiDatabase(r.Context(), func(ctx context.Context) (location, error) {
		if !draftCacheHintSupported {
			path, name, err := s.Store.ArtifactPath(ctx, r.PathValue("id"), owner)
			return location{path: path, name: name}, err
		}
		path, name, draft, err := s.Store.artifactDownloadMetadata(ctx, r.PathValue("id"), owner)
		return location{path: path, name: name, draft: draft}, err
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	// Large results can outlive the server's ordinary ten-minute response limit.
	// ServeContent retains HTTP range/If-Range support for interrupted downloads.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(12 * time.Hour))
	serveFileWithDraftHint(w, r, result.path, result.name, true, result.draft)
}

func serveFile(w http.ResponseWriter, r *http.Request, path, name string, attachment bool) {
	serveFileWithDraftHint(w, r, path, name, attachment, false)
}

func serveFileWithDraftHint(w http.ResponseWriter, r *http.Request, path, name string, attachment, draft bool) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, 404, "expired", "The file has expired or is no longer available")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		internalError(w, err)
		return
	}
	if attachment {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	buffer := make([]byte, 512)
	n, _ := f.Read(buffer)
	_, _ = f.Seek(0, io.SeekStart)
	contentType := http.DetectContentType(buffer[:n])
	if !strings.HasPrefix(contentType, "video/") && !strings.HasPrefix(contentType, "audio/") && contentType != "application/ogg" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if !attachment {
		privateSourceFileHeaders(w, name, info)
	}
	if attachment && draft && draftCacheHintSupported {
		serveDraftArtifactContent(w, r, name, info, f, discardCompletedDraftCache)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), contextReadSeeker{r.Context(), f})
}
