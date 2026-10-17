package app

import (
	"context"
	"errors"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Small, reusable intervals keep seeking independent of recording length.
// Cached files remain private and charged to the durable storage ledger.
func (s *Server) sourcePreview(w http.ResponseWriter, r *http.Request, owner string) {
	s.sourcePreviewWithPublisher(w, r, owner, s.Store.publishPreview)
}

func (s *Server) sourcePreviewWithPublisher(w http.ResponseWriter, r *http.Request, owner string, publish func(context.Context, Config, Source, string, *os.File) error) {
	s.sourceWindowWithPublisher(w, r, owner, false, publish)
}

func (s *Server) sourceWindowWithPublisher(w http.ResponseWriter, r *http.Request, owner string, storyboard bool, publish func(context.Context, Config, Source, string, *os.File) error) {
	s.sourceWindowWithAdmission(w, r, owner, storyboard, publish, s.Store.acquirePreviewAfterCacheLookup)
}

func (s *Server) sourceWindowWithAdmission(w http.ResponseWriter, r *http.Request, owner string, storyboard bool, publish func(context.Context, Config, Source, string, *os.File) error, acquire func(context.Context, Config, Source, string, string, bool, bool) (*os.File, error)) {
	kind := windowVideo
	if storyboard {
		kind = windowStoryboard
	}
	s.sourceAnalysisWithAdmission(w, r, owner, kind, publish, acquire)
}

func (s *Server) sourceAnalysisWithAdmission(w http.ResponseWriter, r *http.Request, owner string, kind windowKind, publish func(context.Context, Config, Source, string, *os.File) error, acquire func(context.Context, Config, Source, string, string, bool, bool) (*os.File, error)) {
	storyboard := kind == windowStoryboard
	waveform := kind == windowWaveform
	ctx, cancel := context.WithTimeout(r.Context(), previewTimeout)
	defer cancel()
	v, err := s.databaseSource(ctx, r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if v.MediaRemoved {
		sourceMediaError(w, ErrSourceMediaRemoved)
		return
	}
	readCtx, releaseRead, err := s.pinSourceMediaReader(ctx, v)
	if err != nil {
		sourceMediaError(w, err)
		return
	}
	defer releaseRead()
	ctx = readCtx
	r = r.WithContext(ctx)
	start, err := strconv.ParseInt(r.URL.Query().Get("start_ms"), 10, 64)
	if err != nil || start < 0 || start >= v.DurationMS {
		writeError(w, 400, "invalid_preview", "Choose a position inside the video")
		return
	}
	request := ExportRequest{}
	query := r.URL.Query()
	windowMS := previewWindowMS
	if values, ok := query["window_ms"]; ok {
		if kind != windowVideo || len(values) != 1 || (values[0] != "6000" && values[0] != "30000") {
			writeError(w, 400, "invalid_preview", "Choose a 6000 or 30000 millisecond video window")
			return
		}
		windowMS, _ = strconv.ParseInt(values[0], 10, 64)
	}
	if value, ok := query["audio_track"]; ok {
		if len(value) != 1 {
			writeError(w, 400, "invalid_preview", "Choose one audio stream index")
			return
		}
		index, e := strconv.Atoi(value[0])
		if e != nil || index < 0 {
			writeError(w, 400, "invalid_preview", "Choose a nonnegative audio stream index")
			return
		}
		request.AudioTrack = &index
	}
	if value, ok := query["no_audio"]; ok {
		if len(value) != 1 || (value[0] != "true" && value[0] != "false") {
			writeError(w, 400, "invalid_preview", "no_audio must be true or false")
			return
		}
		request.NoAudio = value[0] == "true"
	}
	if request.NoAudio && request.AudioTrack != nil {
		writeError(w, 400, "invalid_preview", "Choose an audio stream or no audio")
		return
	}
	if request.AudioTrack != nil && v.Path == "" {
		writeError(w, 422, "audio_track_unavailable", "Selecting an audio track requires an uploaded source")
		return
	}
	if !s.allow("preview:"+owner, 60) {
		writeError(w, 429, "rate_limit", "Wait before requesting another preview")
		return
	}
	// Canonical intervals make nearby seeks reuse the same result.
	start = start / windowMS * windowMS
	key := "display-v1:" + previewCacheKeyOptions(v, start, request)
	if windowMS != previewWindowMS {
		key = "quick-v1:" + previewCacheKeyOptions(v, start, request)
	}
	filename := "preview.mp4"
	outputLimit := previewOutputBytes
	renderWindow := renderPreviewOptions
	if storyboard {
		// Images depend on the video only. Avoid downloading/probing separate
		// platform audio and avoid duplicate images for each audio selection.
		request = ExportRequest{NoAudio: true}
		key = "storyboard:" + previewCacheKeyOptions(v, start, request)
		filename, outputLimit, renderWindow = "storyboard.jpg", storyboardOutputBytes, renderStoryboardOptions
	} else if waveform {
		key = "waveform:frequency-v3:" + previewCacheKeyOptions(v, start, request)
		filename, outputLimit, renderWindow = "waveform.json", waveformOutputBytes, renderWaveformOptions
	}
	id := newID("preview")
	var cached *os.File
	inspected := v.Path == ""
	if v.Path != "" {
		var session *LocalExportSession
		ctx, session, inspected = s.Store.beginCachedLocal(ctx, s.Config, v)
		defer session.Close()
		if inspected {
			v, err = s.Store.refreshLocalMediaDimensions(ctx, s.Config, v)
			if err != nil {
				sourceMediaError(w, err)
				return
			}
		}
	}
	admissionStarted := time.Now()
	waitForForeground := func(err error) bool {
		// A short foreground request may arrive with its source-track probe.
		// Wait briefly in the bounded admission pool instead of making the
		// browser restart the entire request. Background and legacy callers
		// retain immediate refusal; requests still share the same media caps.
		if windowMS != previewQuickWindowMS || query.Get("priority") == "background" || !errors.Is(err, errSourceServerBusy) || time.Since(admissionStarted) >= time.Second {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
			return true
		}
	}
	for {
		cached, err = apiDatabase(ctx, func(dbCtx context.Context) (*os.File, error) {
			return acquire(dbCtx, s.Config, v, id, key, kind != windowVideo || r.URL.Query().Get("priority") == "background", !inspected)
		})
		if errors.Is(err, errPreviewNeedsInspection) {
			// Cache hits need neither tool. A miss releases the storage fence
			// before inspecting, then repeats cache lookup and admission with
			// fresh persisted dimensions for every active preview's weight.
			// Probing opens a decoder too. Keep inspection in the same global
			// pool and pin its source; historical rows can describe a cover
			// image, so conservatively use the full pool until fresh dimensions
			// are known. Release the short inspection lease before admission.
			inspectionSource := v
			inspectionSource.Width, inspectionSource.Height = 0, 0
			// A parallel track request can publish its verified probe while this
			// foreground request waits for admission. Reuse it before probing.
			cachedCtx, candidate, hit := s.Store.beginCachedLocal(ctx, s.Config, v)
			release := func() {}
			var inspectionErr error
			if hit {
				ctx = cachedCtx
				defer candidate.Close()
			} else {
				_ = candidate.Close()
				release, inspectionErr = s.pinSourceInspection(ctx, inspectionSource, previewTimeout)
			}
			err = inspectionErr
			if err == nil {
				v, err = s.Store.refreshLocalMediaDimensions(ctx, s.Config, v)
				if err == nil {
					s.Store.rememberLocalProbe(ctx, s.Config, v)
				}
				release()
			}
			if err != nil {
				if waitForForeground(err) {
					continue
				}
				if r.Context().Err() != nil {
					return
				}
				if errors.Is(err, ErrNotFound) {
					lookupError(w, err)
				} else if errors.Is(err, errDatabaseUnavailable) || errors.Is(err, errSourceServerBusy) || sourceTimedOut(ctx, err) {
					writeSourceAdmissionError(w, ctx, err)
				} else {
					problem := exportProblem(err)
					writeError(w, 422, problem.code, problem.message)
				}
				return
			}
			inspected = true
			continue
		}
		if waitForForeground(err) {
			continue
		}
		if !errors.Is(err, errPreviewPending) {
			break
		}
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				writeError(w, 504, "source_timeout", "Preview preparation timed out; try again")
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		if errors.Is(err, errPreviewAdmissionUncertain) {
			s.releasePreviewAdmission(v, id, true)
		}
		if errors.Is(err, ErrNotFound) {
			lookupError(w, err)
			return
		}
		writeSourceAdmissionError(w, ctx, err)
		return
	}
	rangeMS := Range{StartMS: start, EndMS: min(start+windowMS, v.DurationMS)}
	if cached != nil {
		defer cached.Close()
		serveAnalysisWindow(w, r, cached, rangeMS, "hit", kind)
		return
	}
	dir := filepath.Join(s.Config.DataDir, "previews", id)
	retain := false
	defer func() {
		if retain {
			return
		}
		cleanupCtx, stop := context.WithTimeout(context.Background(), workerDatabaseTimeout)
		defer stop()
		// Keep the reservation if removal failed; maintenance retries it.
		_ = s.Store.releasePreviewAdmission(cleanupCtx, s.Config, v, id, true)
	}()
	if err = fsdurable.EnsureDirectory(dir, 0700); err != nil {
		internalError(w, err)
		return
	}
	if err = fsdurable.Preflight(dir, dir); err != nil {
		internalError(w, err)
		return
	}
	c := s.Config
	c.MaxSourceBytes, c.MaxFetchBytes, c.MaxOutputBytes = previewInputBytes, previewInputBytes, outputLimit
	out := filepath.Join(dir, "staging."+filename)
	inputs := []mediaInput{{Path: v.Path}}
	if v.Path == "" {
		guard, e := newNetworkGuard(previewInputBytes)
		if e != nil {
			internalError(w, e)
			return
		}
		defer guard.Close()
		render := func(fresh bool) error {
			request.Quality, request.Format, request.CutMode = "480p", "mp4", "accurate"
			if waveform {
				request.Format = "mp3"
			}
			resolved, streams, playlists, _, e := platformInputs(ctx, c, s.Store, v, guard, request, fresh)
			if e != nil {
				return e
			}
			for i, stream := range streams {
				if !isHLS(stream) {
					continue
				}
				// A retry uses a separate filename and the same transfer budget.
				index := i
				if fresh {
					index += 10
				}
				path, offset, e := stageHLS(ctx, c, guard, stream, playlists[i], rangeMS, dir, index)
				if e != nil {
					return e
				}
				resolved[i] = mediaInput{Path: path, OffsetMS: offset}
			}
			return renderWindow(ctx, c, resolved, rangeMS, out, request)
		}
		err = render(false)
		if cachedAddressDenied(err) && ctx.Err() == nil {
			err = render(true)
		}
	} else {
		err = renderWindow(ctx, c, inputs, rangeMS, out, request)
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		problem := exportProblem(err)
		writeError(w, 422, problem.code, problem.message)
		return
	}
	final := filepath.Join(dir, filename)
	if _, err = fsdurable.Publish(ctx, out, final); err != nil {
		internalError(w, err)
		return
	}
	// Release staged inputs before turning the processing reservation into a
	// cache entry charged at the actual MP4 size. Keep a file descriptor open:
	// eviction/source deletion can unlink the file without interrupting a read.
	file, err := os.Open(final)
	if err != nil {
		internalError(w, err)
		return
	}
	defer file.Close()
	if err = trimWindowWorkspace(ctx, dir, filename); err != nil {
		internalError(w, err)
		return
	}
	// A failed COMMIT can have succeeded remotely. Preserve the bounded lease
	// until maintenance decides its state instead of deleting a published file.
	if err = publish(ctx, s.Config, v, id, file); err != nil {
		retain = errors.Is(err, ErrPreviewCommitUncertain)
		internalError(w, err)
		return
	}
	retain = true
	serveAnalysisWindow(w, r, file, rangeMS, "miss", kind)
}

func servePreview(w http.ResponseWriter, r *http.Request, file *os.File, interval Range, cache string) {
	serveWindow(w, r, file, interval, cache, false)
}

func serveWindow(w http.ResponseWriter, r *http.Request, file *os.File, interval Range, cache string, storyboard bool) {
	kind := windowVideo
	if storyboard {
		kind = windowStoryboard
	}
	serveAnalysisWindow(w, r, file, interval, cache, kind)
}

func serveAnalysisWindow(w http.ResponseWriter, r *http.Request, file *os.File, interval Range, cache string, kind windowKind) {
	storyboard := kind == windowStoryboard
	info, err := file.Stat()
	if err != nil {
		internalError(w, err)
		return
	}
	// The browser's bounded blob cache is session-local. HTTP/shared caches
	// must never bypass source ownership checks or retain private URLs.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "video/mp4")
	if storyboard {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-Storyboard-Step-MS", strconv.FormatInt(storyboardStepMS, 10))
		w.Header().Set("X-Storyboard-Frames", strconv.FormatInt((interval.EndMS-interval.StartMS+storyboardStepMS-1)/storyboardStepMS, 10))
	}
	if kind == windowWaveform {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Waveform-Step-MS", strconv.FormatInt(waveformStepMS, 10))
	}
	if kind == windowVideo {
		if width, height := readPreviewGeometry(file, info.Size()); width > 0 && height > 0 {
			w.Header().Set("X-Source-Display-Width", strconv.Itoa(width))
			w.Header().Set("X-Source-Display-Height", strconv.Itoa(height))
		}
	}
	w.Header().Set("X-Preview-Cache", cache)
	w.Header().Set("X-Preview-Start-MS", strconv.FormatInt(interval.StartMS, 10))
	w.Header().Set("X-Preview-End-MS", strconv.FormatInt(interval.EndMS, 10))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	http.ServeContent(w, r, "preview.mp4", info.ModTime(), contextReadSeeker{r.Context(), file})
}
