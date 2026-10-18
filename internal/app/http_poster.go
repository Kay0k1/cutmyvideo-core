package app

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func (s *Server) createPoster(w http.ResponseWriter, r *http.Request, owner string) {
	var request *struct {
		TimestampMS *int64 `json:"timestamp_ms"`
		Format      string `json:"format"`
	}
	if err := decodeJSONLimit(w, r, &request, 4096); err != nil {
		return
	}
	if request == nil || request.TimestampMS == nil {
		writeError(w, 400, "invalid_poster", "An explicit output timestamp_ms is required")
		return
	}
	o, err := localmedia.NormalizePosterOptions(localmedia.PosterOptions{TimestampMS: *request.TimestampMS, Format: request.Format})
	if err != nil {
		writePosterError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), localmedia.PosterTimeout)
	defer cancel()
	// Retained exact requests are returned before current decoder/storage limits.
	existing, err := apiDatabase(ctx, func(ctx context.Context) (ImageArtifact, error) {
		a, _, err := scanImage(s.Store.DB.QueryRow(ctx, imageArtifactSelect+" AND i.parent_artifact_id=$2 AND i.timestamp_ms=$3 AND i.format=$4", owner, r.PathValue("id"), o.TimestampMS, o.Format))
		return a, err
	})
	if err == nil {
		writeJSON(w, 200, existing)
		return
	}
	if !errors.Is(err, ErrNotFound) {
		writePosterError(w, r, err)
		return
	}
	if !s.allow("poster:"+owner, 6) {
		writeError(w, 429, "rate_limit", "Wait before requesting another poster")
		return
	}
	id := newID("img")
	type admission struct {
		lease    posterLease
		existing *ImageArtifact
	}
	accepted, err := apiDatabase(ctx, func(ctx context.Context) (admission, error) {
		l, a, e := s.Store.acquirePoster(ctx, s.Config, owner, r.PathValue("id"), id, o)
		return admission{l, a}, e
	})
	if accepted.lease.id != "" {
		defer func() {
			releaseCtx, stop := context.WithTimeout(context.Background(), workerDatabaseTimeout)
			defer stop()
			_ = s.Store.releasePoster(releaseCtx, s.Config, accepted.lease)
		}()
	}
	if err != nil {
		writePosterError(w, r, err)
		return
	}
	if accepted.existing != nil {
		writeJSON(w, 200, accepted.existing)
		return
	}
	lease := accepted.lease
	before, err := openPosterParent(s.Config, lease.path)
	if err != nil {
		writePosterError(w, r, err)
		return
	}
	dir := posterWorkspace(s.Config, id)
	if err = fsdurable.EnsureDirectory(dir, 0700); err != nil {
		writePosterError(w, r, err)
		return
	}
	if err = fsdurable.EnsureDirectory(filepath.Join(s.Config.DataDir, "artifacts"), 0700); err != nil {
		writePosterError(w, r, err)
		return
	}
	if err = fsdurable.Preflight(dir, filepath.Join(s.Config.DataDir, "artifacts")); err != nil {
		writePosterError(w, r, err)
		return
	}
	result, err := localmedia.GeneratePoster(ctx, s.Config, lease.path, filepath.Join(dir, "poster.part"), o)
	if err != nil {
		writePosterError(w, r, err)
		return
	}
	after, err := openPosterParent(s.Config, lease.path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		writePosterError(w, r, ErrNotFound)
		return
	}
	final := posterPath(s.Config, id, o.Format)
	if _, err = fsdurable.Publish(ctx, result.Path, final); err != nil {
		writePosterError(w, r, err)
		return
	}
	result.Path = final
	a, err := apiDatabase(ctx, func(ctx context.Context) (ImageArtifact, error) {
		return s.Store.publishPoster(ctx, s.Config, lease, result)
	})
	if err != nil {
		writePosterError(w, r, err)
		return
	}
	writeJSON(w, 201, a)
}

func writePosterError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, os.ErrNotExist):
		writeError(w, 404, "not_found", "The completed video artifact is unavailable")
	case errors.Is(err, localmedia.ErrPosterFrameUnavailable):
		writeError(w, 400, "invalid_poster", "No decodable frame exists at this moment; choose a slightly earlier timestamp")
	case errors.Is(err, localmedia.ErrInvalidPoster):
		writeError(w, 400, "invalid_poster", "Choose JPEG or PNG and a timestamp inside a completed video")
	case errors.Is(err, localmedia.ErrPosterTooLarge):
		writeError(w, 413, "poster_limit", "The poster exceeds the decoded pixel or output byte limit")
	case errors.Is(err, errSourceServerBusy), errors.Is(err, errPreviewPending):
		writeError(w, 503, "media_busy", "The media decoder is busy; retry later")
	case errors.Is(err, errSourceStorage):
		writeError(w, 507, "storage_unavailable", "The poster cannot fit the configured storage quota")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, 504, "poster_timeout", "Poster generation exceeded its time limit")
	default:
		internalError(w, err)
	}
}

func (s *Server) getImageArtifact(w http.ResponseWriter, r *http.Request, owner string) {
	a, err := apiDatabase(r.Context(), func(ctx context.Context) (ImageArtifact, error) {
		a, _, e := s.Store.imageArtifact(ctx, r.PathValue("id"), owner)
		return a, e
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) downloadImageArtifact(w http.ResponseWriter, r *http.Request, owner string) {
	// Open under the metadata-deletion fence. Once open, ServeContent can safely
	// finish a read after unlink; failed removals retain their ordinary charge.
	type opened struct {
		a ImageArtifact
		f *os.File
	}
	value, err := apiDatabase(r.Context(), func(ctx context.Context) (opened, error) {
		tx, e := s.Store.storageTx(ctx)
		if e != nil {
			return opened{}, e
		}
		defer rollbackStorage(tx)
		a, path, e := scanImage(tx.QueryRow(ctx, imageArtifactSelect+" AND i.id=$2", owner, r.PathValue("id")))
		if e != nil {
			return opened{}, e
		}
		if _, e = openPosterParent(s.Config, path); e != nil {
			return opened{}, e
		}
		f, e := os.Open(path)
		if e != nil {
			return opened{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			f.Close()
			return opened{}, e
		}
		return opened{a, f}, nil
	})
	if err != nil {
		writePosterError(w, r, err)
		return
	}
	defer value.f.Close()
	info, err := value.f.Stat()
	if err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/"+value.a.Format)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": value.a.Filename}))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, value.a.Filename, info.ModTime(), contextReadSeeker{r.Context(), value.f})
}

func (s *Server) deleteImageArtifact(w http.ResponseWriter, r *http.Request, owner string) {
	paths, err := apiDatabase(r.Context(), func(ctx context.Context) ([]string, error) {
		return s.Store.deleteImageArtifact(ctx, r.PathValue("id"), owner)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), workerDatabaseTimeout)
	defer cancel()
	_ = s.Store.DrainStorageDeletes(ctx, s.Config, paths)
	w.WriteHeader(http.StatusNoContent)
}
