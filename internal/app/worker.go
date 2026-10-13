package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

func RunWorker(ctx context.Context, c Config, s *Store) error {
	s.ConfigureStorage(c)
	ctx = withWorkerMediaBudget(ctx, c)
	health, err := newWorkerHealth(c.WorkerHealthPath)
	if err != nil {
		return err
	}
	defer health.Close()
	if err := fsdurable.EnsureDirectory(filepath.Join(c.DataDir, "artifacts"), 0700); err != nil {
		return err
	}
	if err := fsdurable.EnsureDirectory(filepath.Join(c.DataDir, "work"), 0700); err != nil {
		return err
	}
	if err := fsdurable.Preflight(filepath.Join(c.DataDir, "work"), filepath.Join(c.DataDir, "artifacts")); err != nil {
		return err
	}
	if err := fsdurable.SyncDirectories(c.DataDir, filepath.Dir(c.DataDir)); err != nil {
		return err
	}
	if err := recoverWorkerJobs(ctx, s); err != nil {
		return err
	}
	stopMaintenance := startWorkerMaintenance(ctx, c, s)
	defer stopMaintenance()
	wakeup := startWorkerWakeup(ctx, s)
	defer wakeup.close()
	runWorkerPipelinePool(ctx, c, func() {
		for {
			if ctx.Err() != nil {
				return
			}
			// Capture before claiming so a commit between the empty claim and
			// waiting cannot lose its wakeup. The lease still decides ownership.
			notified := wakeup.snapshot()
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, workerDatabaseTimeout)
			if err := s.ProcessSourceMediaCleanup(cleanupCtx, c); err != nil {
				slog.Warn("source media cleanup pending", "error", err)
			}
			cleanupCancel()
			claimStarted := time.Now()
			job, token, err := claimWorkerJob(ctx, s)
			if errors.Is(err, ErrNotFound) {
				health.Touch()
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				case <-notified:
				}
				continue
			}
			if err != nil {
				slog.Error("job claim failed", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			health.Touch()
			slog.Info("job_claimed", "job_id", job.ID, "claim_elapsed_ms", time.Since(claimStarted).Milliseconds())
			processJob(ctx, c, s, job, token)
		}
	})
	return nil
}

func processJob(parent context.Context, c Config, s *Store, j Job, token string) {
	processJobWithPublisher(parent, c, s, j, token, s.PublishArtifact)
}

func processJobWithPublisher(parent context.Context, c Config, s *Store, j Job, token string, publish func(context.Context, Job, string, string, Artifact) error) {
	started := time.Now()
	work := filepath.Join(c.DataDir, "work", j.ID+"-"+token)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), workerDatabaseTimeout)
		defer cleanupCancel()
		if err := removeStorageTree(cleanupCtx, work); err != nil {
			slog.Error("workspace cleanup failed", "job_id", j.ID, "error", err)
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), workerDatabaseTimeout)
		defer cancel()
		if err := s.ReleaseJobStorage(releaseCtx, j.ID, token); err != nil {
			slog.Error("job storage release failed", "job_id", j.ID, "error", err)
		}
	}()
	defer func() {
		slog.Info("job_processing_stopped", "job_id", j.ID, "status", j.Status, "stage", j.Stage, "elapsed_ms", time.Since(started).Milliseconds())
	}()
	ctx, cancel := context.WithTimeout(parent, c.JobTimeout)
	ctx = withMediaDiagnostics(ctx, c, j.ID, "")
	defer cancel()
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	var leaseUncertain atomic.Bool
	go func(heartbeatCtx context.Context) {
		defer close(heartbeatDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				cancelled, err := heartbeatWorkerJob(heartbeatCtx, s, j.ID, token)
				if cancelled || err != nil {
					if err != nil && heartbeatCtx.Err() == nil {
						leaseUncertain.Store(true)
					}
					cancel()
					return
				}
				touchWorkerHealth(c.WorkerHealthPath)
			}
		}
	}(ctx)
	defer func() { close(stopHeartbeat); <-heartbeatDone }()
	persist := func() bool {
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer saveCancel()
		err := s.SaveJob(saveCtx, j, token)
		if err != nil {
			slog.Error("job save failed", "job_id", j.ID, "error", err)
			leaseUncertain.Store(true)
			cancel()
			return false
		}
		return true
	}
	finishFailure := func(code, message string) {
		// A deployment or worker shutdown leaves the leased job recoverable.
		// Job deadlines and explicit user cancellation still settle normally.
		if parent.Err() != nil || leaseUncertain.Load() {
			return
		}
		j.Status = "failed"
		j.Stage = "finished"
		j.Message = message
		readCtx, readCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		current, err := s.Job(readCtx, j.ID, j.Owner)
		readCancel()
		if err == nil && current.Cancelled {
			j.Status = "cancelled"
			j.Message = "Cancelled"
			code = "cancelled"
		}
		for i := range j.Items {
			if j.Items[i].Status == "queued" || j.Items[i].Status == "running" {
				j.Items[i].Status = j.Status
				j.Items[i].Message = j.Message
				j.Items[i].ErrorCode = code
			}
		}
		persist()
	}
	// An acknowledged/lost-ack publication may leave only the terminal
	// snapshot unsaved. Existing completed results need no new media resolution.
	completed := len(j.Items) > 0
	lastUnfinished := -1
	for i, item := range j.Items {
		if item.Status != "succeeded" || item.Artifact == nil {
			completed = false
			lastUnfinished = i
			continue
		}
		path, _, e := s.ArtifactPath(ctx, item.Artifact.ID, j.Owner)
		if e != nil {
			completed = false
			lastUnfinished = i
			continue
		}
		if _, e = os.Stat(path); e != nil {
			completed = false
			lastUnfinished = i
		}
	}
	if completed {
		j.Status, j.Stage, j.Message = "succeeded", "finished", "All fragments are ready"
		persist()
		return
	}
	source, err := s.Source(ctx, j.Request.SourceID, j.Owner)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			finishFailure("source_expired", "Source is unavailable or has expired")
		} else if ctx.Err() != nil {
			finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
		} else {
			finishFailure("server_error", "Could not read the source; try exporting again")
		}
		return
	}
	if source.MediaRemoved {
		finishFailure("source_removed", "Original recording was removed; reopen it to continue editing")
		return
	}
	inputs := []mediaInput{{Path: source.Path}}
	var streams []platformFormat
	var playlists []hlsPlaylist
	var guard *networkGuard
	cachedMetadata, cacheRefreshUsed := false, false
	if source.Path == "" {
		j.Stage = "resolving"
		j.Message = "Resolving source streams"
		if !persist() {
			return
		}
		guard, err = newNetworkGuard(remoteSourceBudget(c))
		if err != nil {
			finishFailure("server_error", "Could not prepare source access")
			return
		}
		defer guard.Close()
		resolvedAt := time.Now()
		resolveCtx, resolveCancel := context.WithTimeout(ctx, c.SourceTimeout)
		inputs, streams, playlists, cachedMetadata, err = platformInputs(resolveCtx, c, s, source, guard, j.Request, false)
		if err != nil && cachedMetadata && cachedAddressDenied(err) && resolveCtx.Err() == nil {
			cacheRefreshUsed = true
			inputs, streams, playlists, cachedMetadata, err = platformInputs(resolveCtx, c, s, source, guard, j.Request, true)
		}
		resolveCancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				finishFailure("source_timeout", "Source inspection timed out; open the video again or upload your file")
			} else {
				problem := exportProblem(err)
				finishFailure(problem.code, problem.message)
			}
			return
		}
		slog.Info("source_streams_resolved", "job_id", j.ID, "cache_hit", cachedMetadata, "elapsed_ms", time.Since(resolvedAt).Milliseconds())
	}
	var localSession *LocalExportSession
	if source.Path != "" {
		inspectionStarted := time.Now()
		var cached bool
		ctx, localSession, cached = s.beginCachedLocal(ctx, c, source)
		localSession.SetWorkspace(work)
		localSession.SetRanges(j.Request.Ranges)
		defer localSession.Close()
		releaseInspection := func() {}
		if !cached {
			// Historical dimensions may describe an attached picture. A cold
			// probe therefore reserves the full decoder budget conservatively.
			releaseInspection, err = acquireMediaInspection(ctx, Source{Path: source.Path}, nil)
		}
		if err == nil {
			source, err = s.refreshLocalMediaDimensions(ctx, c, source)
		}
		if releaseInspection != nil {
			releaseInspection()
		}
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				finishFailure("source_expired", "Source is unavailable or has expired")
			} else if errors.Is(err, errDatabaseUnavailable) {
				finishFailure("server_error", "Could not refresh source metadata; try exporting again")
			} else if storagePressureError(err) {
				if !waitForJobStorage(parent, c, s, &j, token, work) {
					finishFailure("server_error", "Could not wait for storage")
				}
			} else if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				finishFailure("source_timeout", "Source inspection timed out; try exporting again")
			} else if ctx.Err() != nil {
				finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
			} else {
				problem := exportProblem(err)
				finishFailure(problem.code, problem.message)
			}
			return
		}
		s.rememberLocalProbe(ctx, c, source)
		slog.Info("source_inspected", "job_id", j.ID, "cache_hit", cached, "elapsed_ms", time.Since(inspectionStarted).Milliseconds())
	}

	var mediaPermit mediaProcessingPermit
	if !pipelineMediaBudget(ctx) {
		waitStarted := time.Now()
		if budgetErr := mediaPermit.Acquire(ctx, source, streams, j.Request); budgetErr != nil {
			finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
			return
		}
		slog.Info("media_processing_wait", "job_id", j.ID, "elapsed_ms", time.Since(waitStarted).Milliseconds(), "scope", "job")
	}
	defer mediaPermit.Release()

	work = filepath.Join(c.DataDir, "work", j.ID+"-"+token)
	for _, directory := range []string{work, filepath.Join(c.DataDir, "artifacts")} {
		if err = os.MkdirAll(directory, 0700); err != nil {
			if storagePressureError(err) {
				if !waitForJobStorage(parent, c, s, &j, token, work) {
					finishFailure("server_error", "Could not wait for storage")
				}
				return
			}
			finishFailure("server_error", "Could not initialize result storage")
			return
		}
	}
	if err = fsdurable.Preflight(work, filepath.Join(c.DataDir, "artifacts")); err != nil {
		if storagePressureError(err) {
			if !waitForJobStorage(parent, c, s, &j, token, work) {
				finishFailure("server_error", "Could not wait for storage")
			}
			return
		}
		if ctx.Err() != nil || leaseUncertain.Load() {
			finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
			return
		}
		finishFailure("server_error", "Storage does not permit reliable result publication")
		return
	}
	if source.Path == "" {
		var closeCache func()
		ctx, closeCache, err = withHLSDownloadCache(ctx, work, hlsJobCacheBudget(c))
		if err != nil {
			if storagePressureError(err) {
				if !waitForJobStorage(parent, c, s, &j, token, work) {
					finishFailure("server_error", "Could not wait for storage")
				}
			} else {
				finishFailure("server_error", "Could not initialize source cache")
			}
			return
		}
		defer closeCache()
	}
	var sourceManifest []TrackSelection
	if source.Path != "" {
		sourceManifest, err = SelectExportTracks(source.Tracks, j.Request)
		if err != nil {
			problem := exportProblem(err)
			finishFailure(problem.code, problem.message)
			return
		}
	}
	type verifiedLocalResult struct {
		path     string
		info     os.FileInfo
		artifact Artifact
	}
	// Only this attempt's acknowledged publications are reusable. Recovered
	// artifacts may predate this operation's verified source identity.
	localResults := make(map[[2]int64]verifiedLocalResult)
	for i := range j.Items {
		item := &j.Items[i]
		itemCtx := withMediaDiagnostics(ctx, c, j.ID, item.ID)
		joined := j.Request.OutputMode == "joined"
		selected := j.Request.Ranges[i]
		if joined {
			selected = joinedOutputRange(j.Request)
		}
		if item.Status == "succeeded" && item.Artifact != nil {
			if path, _, e := s.ArtifactPath(ctx, item.Artifact.ID, j.Owner); e == nil {
				if _, e = os.Stat(path); e == nil {
					continue
				}
			}
		}
		if ctx.Err() != nil {
			finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
			return
		}
		item.Status = "running"
		item.Message = ""
		item.ErrorCode = ""
		item.Artifact = nil
		item.ProgressMS = 0
		j.Stage = "processing"
		j.Message = fmt.Sprintf("Processing fragment %d of %d", i+1, len(j.Items))
		if !persist() {
			return
		}
		out := filepath.Join(work, item.ID+"."+j.Request.Format)
		encodeStarted := time.Now()
		var start, end int64
		var segments []ArtifactSegment
		trackManifest := sourceManifest
		var e error
		var lastProgressSave time.Time
		persistProgress := func() {
			saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(parent), workerDatabaseTimeout)
			defer saveCancel()
			if err := s.SaveJobProgress(saveCtx, j.ID, token, j.Stage, j.Message, i, item.ProgressMS); err != nil {
				slog.Error("job progress save failed", "job_id", j.ID, "error", err)
				leaseUncertain.Store(true)
				cancel()
			}
		}
		report := func(ms int64) {
			if duration := item.EndMS - item.StartMS; ms > duration {
				ms = duration
			}
			item.ProgressMS = ms
			if time.Since(lastProgressSave) >= time.Second {
				lastProgressSave = time.Now()
				persistProgress()
			}
		}
		for {
			storageOK := true
			if c.MaxStorageBytes > 0 && s.storageConfig().MaxStorageBytes > 0 {
				var storageErr error
				storageOK, storageErr = s.JobStorageAvailable(ctx, c, j.ID, token)
				if storageErr != nil {
					finishFailure("server_error", "Could not check storage reservation")
					return
				}
			} else {
				reserve := outputBudget(c, selected, j.Request)
				if !joined {
					subtitles, reserveErr := subtitleWorkBudget(c, j.Request, reserve)
					if reserveErr != nil || subtitles > (1<<63-1)-reserve {
						storageOK = false
					} else {
						reserve += subtitles
					}
				}
				if joined {
					var reserveErr error
					reserve, reserveErr = joinedWorkBudget(c, j.Request, len(streams) > 0)
					storageOK = reserveErr == nil
				}
				for _, f := range streams {
					if joined {
						break
					}
					if isHLS(f) {
						if inputBudget(c, j.Request.Ranges[i], j.Request) > ((1<<63-1)-reserve)/2 {
							storageOK = false
						} else {
							reserve += 2 * inputBudget(c, j.Request.Ranges[i], j.Request)
						}
						break
					}
				}
				storageOK = storageOK && storageAvailableContext(ctx, c, reserve)
			}
			if !storageOK {
				if ctx.Err() != nil {
					finishFailure("job_timeout", "Processing exceeded the time limit")
					return
				}
				// A later external disk fill leaves the accepted export waiting, rather
				// than terminating it. Remove work before relinquishing its reservation.
				if !waitForJobStorage(parent, c, s, &j, token, work) {
					finishFailure("server_error", "Could not wait for storage")
				}

				return
			}
			fragmentDir := filepath.Join(work, item.ID)
			ranges := []Range{selected}
			if joined {
				ranges = j.Request.Ranges
			}
			preparationStarted := time.Now()
			var networkBefore int64
			if guard != nil {
				networkBefore = guard.bytes.Load()
			}
			segmentInputs, stageErr := stageWorkerSegments(itemCtx, c, j.Request, guard, streams, playlists, inputs, ranges, fragmentDir, func(segmentIndex, done, total int) {
				j.Stage = "fetching"
				fragment, count := i+1, len(j.Items)
				if joined {
					fragment, count = segmentIndex+1, len(ranges)
				}
				j.Message = fmt.Sprintf("Preparing fragment %d of %d: %d%% downloaded", fragment, count, done*100/max(1, total))
				if time.Since(lastProgressSave) >= time.Second {
					lastProgressSave = time.Now()
					persistProgress()
				}
			})
			var networkBytes int64
			if guard != nil {
				networkBytes = guard.bytes.Load() - networkBefore
			}
			slog.Info("source_preparation_finished", "job_id", j.ID, "item_id", item.ID, "elapsed_ms", time.Since(preparationStarted).Milliseconds(), "network_bytes", networkBytes, "success", stageErr == nil)
			e = stageErr

			if e == nil {
				j.Stage = "processing"
				j.Message = fmt.Sprintf("Processing fragment %d of %d", i+1, len(j.Items))
				if joined {
					j.Message = "Joining selected fragments"
				}
				if !persist() {
					return
				}
				reused := false
				processingStarted := time.Now()
				if !joined && source.Path != "" {
					if prior, ok := localResults[[2]int64{selected.StartMS, selected.EndMS}]; ok {
						owned, _, authorizationErr := s.ArtifactPath(ctx, prior.artifact.ID, j.Owner)
						if authorizationErr == nil && owned == prior.path {
							processingStarted = time.Now()
							reused, e = ReuseLocalExport(ctx, source.Path, owned, out, prior.info, outputBudget(c, selected, j.Request))
							if reused {
								start, end = prior.artifact.ActualStartMS, prior.artifact.ActualEndMS
								report(item.EndMS - item.StartMS)
							}
						}
					}
				}
				if e == nil && !reused && pipelineMediaBudget(ctx) {
					waitStarted := time.Now()
					e = mediaPermit.Acquire(ctx, source, streams, j.Request)
					slog.Info("media_processing_wait", "job_id", j.ID, "item_id", item.ID, "elapsed_ms", time.Since(waitStarted).Milliseconds(), "scope", "item", "success", e == nil)
				}
				if !reused {
					processingStarted = time.Now()
				}
				if e == nil && !reused && joined {
					segments, e = exportJoinedSegmentInputsProgress(itemCtx, c, segmentInputs, ranges, j.Request, out, report)
					if e == nil && len(segments) > 0 {
						start, end = 0, segments[len(segments)-1].OutputEndMS
					}
				} else if e == nil && !reused {
					start, end, e = exportInputsProgress(itemCtx, c, segmentInputs[0], selected, j.Request, out, report)
				}
				slog.Info("media_processing_finished", "job_id", j.ID, "item_id", item.ID, "elapsed_ms", time.Since(processingStarted).Milliseconds(), "reused", reused, "success", e == nil)
				if pipelineMediaBudget(ctx) {
					mediaPermit.Release()
				}
			}

			_ = os.RemoveAll(fragmentDir)
			if e == nil || !cachedMetadata || cacheRefreshUsed || !cachedAddressDenied(e) || ctx.Err() != nil {
				break
			}
			cacheRefreshUsed = true
			_ = os.Remove(out)
			j.Stage = "resolving"
			j.Message = "Refreshing source streams"
			item.ProgressMS = 0
			if !persist() {
				return
			}
			inputs, streams, playlists, cachedMetadata, e = platformInputs(ctx, c, s, source, guard, j.Request, true)
			if e != nil {
				break
			}
			if !pipelineMediaBudget(ctx) {
				if e = mediaPermit.Acquire(ctx, source, streams, j.Request); e != nil {
					break
				}
			}
			j.Stage = "processing"
			j.Message = fmt.Sprintf("Processing fragment %d of %d", i+1, len(j.Items))
			if !persist() {
				return
			}
			slog.Info("cached_source_refreshed", "job_id", j.ID)
		}
		slog.Info("fragment_processed", "job_id", j.ID, "item_id", item.ID, "elapsed_ms", time.Since(encodeStarted).Milliseconds(), "media_ms", item.ProgressMS, "success", e == nil)
		// No further interval needs source text after the last unfinished item.
		// Remove it before publication and propagate cleanup failures; work
		// headroom remains fenced until the whole workspace has been removed.
		if localSession != nil && i >= lastUnfinished {
			if cleanupErr := localSession.Close(); cleanupErr != nil {
				e = errors.Join(e, cleanupErr)
			}
		}
		if e != nil {
			if ctx.Err() != nil {
				finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
				return
			}
			if storagePressureError(e) {
				if !waitForJobStorage(parent, c, s, &j, token, work) {
					finishFailure("server_error", "Could not wait for storage")
				}
				return
			}
			item.Status = "failed"
			problem := exportProblem(e)
			item.Message = problem.message
			item.ErrorCode = problem.code
			slog.Warn("fragment failed", "job_id", j.ID, "item_id", item.ID, "error", e)
			_ = os.Remove(out)
			if !persist() {
				return
			}
			continue
		}
		if ctx.Err() != nil || leaseUncertain.Load() {
			finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
			return
		}
		j.Stage = "publishing"
		j.Message = "Saving the fragment"
		if !persist() {
			return
		}
		id := newID("art")
		path := filepath.Join(c.DataDir, "artifacts", id+"."+j.Request.Format)
		publicationStarted := time.Now()
		info, e := fsdurable.Publish(ctx, out, path)
		if e != nil {
			slog.Info("artifact_publication_finished", "job_id", j.ID, "item_id", item.ID, "elapsed_ms", time.Since(publicationStarted).Milliseconds(), "success", false)
			if errors.Is(e, fsdurable.ErrPublicationUncertain) {
				// No result is registered before the filesystem barrier. Retain
				// any uncertain final name for bounded orphan reconciliation.
				slog.Warn("artifact file publication outcome unknown", "job_id", j.ID, "artifact_id", id)
			}
			if storagePressureError(e) {
				if !waitForJobStorage(parent, c, s, &j, token, work) {
					finishFailure("server_error", "Could not wait for storage")
				}
				return
			}
			if ctx.Err() != nil || leaseUncertain.Load() {
				finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
				return
			}
			item.Status = "failed"
			item.Message = "Could not save the result"
			item.ErrorCode = "server_error"
			if !persist() {
				return
			}
			continue
		}
		a := Artifact{ID: id, Filename: fmt.Sprintf("cut-%02d-%s.%s", i+1, item.ID[5:13], j.Request.Format), SizeBytes: info.Size(), DownloadURL: "/api/v1/artifacts/" + id + "/download", ActualStartMS: start, ActualEndMS: end}
		if joined {
			a.Segments = &segments
			a.Filename = "cut-joined-" + item.ID[5:13] + "." + j.Request.Format
		}
		if trackManifest != nil {
			a.TrackManifest = &trackManifest
		}
		previous := *item
		item.Status = "succeeded"
		item.ProgressMS = item.EndMS - item.StartMS
		item.Artifact = &a
		e = publish(ctx, j, path, token, a)
		slog.Info("artifact_publication_finished", "job_id", j.ID, "item_id", item.ID, "elapsed_ms", time.Since(publicationStarted).Milliseconds(), "size_bytes", a.SizeBytes, "success", e == nil)
		if e != nil {
			var publication *ArtifactPublicationError
			if errors.As(e, &publication) && publication.CommitUncertain {
				// PostgreSQL may have committed both writes before the connection
				// failed. Preserve the file and let lease recovery read its state.
				slog.Warn("artifact publication acknowledgement lost", "job_id", j.ID, "item_id", item.ID)
				return
			}
			*item = previous
			_ = os.Remove(path)
			if errors.Is(e, ErrNotFound) {
				finishFailure("server_error", "Could not register the result")
				return
			}
			if ctx.Err() != nil || leaseUncertain.Load() {
				finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
				return
			}
			item.Status = "failed"
			item.Message = "Could not register the result"
			item.ErrorCode = "server_error"
			if !persist() {
				return
			}
			continue
		}
		if !joined && source.Path != "" {
			localResults[[2]int64{selected.StartMS, selected.EndMS}] = verifiedLocalResult{path: path, info: info, artifact: a}
		}
	}
	failed := 0
	for _, item := range j.Items {
		if item.Status != "succeeded" {
			failed++
		}
	}
	j.Stage = "finished"
	j.Status = "succeeded"
	j.Message = "All fragments are ready"
	if failed > 0 {
		if ctx.Err() != nil || leaseUncertain.Load() {
			finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
			return
		}
		j.Status = "failed"
		j.Message = fmt.Sprintf("%d of %d fragments could not be exported; completed files are available", failed, len(j.Items))
	}
	persist()
}

func cleanupFiles(ctx context.Context, c Config, s *Store) error {
	var problems []error
	retentionCtx, retentionCancel := maintenanceStageContext(ctx, 2)
	defer retentionCancel()
	if err := s.cleanupSourceMediaReaders(retentionCtx); err != nil {
		problems = append(problems, err)
	}
	if err := s.ProcessSourceMediaCleanup(retentionCtx, c); err != nil {
		problems = append(problems, err)
	}
	if err := s.cleanupPreviews(retentionCtx, c); err != nil {
		problems = append(problems, err)
	}
	// Drain a busy expiry backlog within the existing maintenance deadline.
	// Every batch releases the shared storage lock for admissions/publications;
	// reconciliation receives its own budget after this bounded retention work.
	for range 8 {
		batch, err := s.cleanupStorageBatch(retentionCtx, c.ArtifactTTL, c.SourceTTL)
		if batch.cacheErr != nil {
			slog.Warn("metadata cache cleanup pending", "error", batch.cacheErr)
			problems = append(problems, batch.cacheErr)
		}
		if batch.auditErr != nil {
			slog.Warn("source pin audit pending", "error", batch.auditErr)
			problems = append(problems, batch.auditErr)
		}
		if err != nil {
			slog.Error("cleanup failed", "error", err)
			problems = append(problems, err)
			break
		}
		if err = s.DrainStorageDeletes(retentionCtx, c, batch.paths); err != nil {
			slog.Warn("media deletion pending", "error", err)
			problems = append(problems, err)
			if !storageDeletionRetryOnly(err) {
				break
			}
		}
		if !batch.full && !batch.auditMore {
			break
		}
	}
	retentionCancel()
	reconcileCtx, reconcileCancel := maintenanceStageContext(ctx, 2)
	if err := s.ReconcileStorage(reconcileCtx, c); err != nil {
		slog.Error("storage reconciliation failed", "error", err)
		problems = append(problems, err)
	}
	reconcileCancel()
	// Pick up orphan tombstones created by reconciliation in the same cycle.
	deletionCtx, deletionCancel := maintenanceStageContext(ctx, 2)
	tx, err := s.storageTx(deletionCtx)
	if err == nil {
		var paths []string
		paths, err = pendingStoragePaths(deletionCtx, tx)
		rollbackStorage(tx)
		if err == nil {
			if deletionErr := s.DrainStorageDeletes(deletionCtx, c, paths); deletionErr != nil {
				problems = append(problems, deletionErr)
			}
		}
	}
	if err != nil {
		problems = append(problems, err)
	}
	deletionCancel()
	if err := s.cleanupWorkProgress(ctx, c); err != nil {
		problems = append(problems, err)
	}

	return errors.Join(problems...)
}

func storageAvailable(c Config, reserve int64) bool {
	return storageAvailableContext(context.Background(), c, reserve)
}

func storageAvailableContext(ctx context.Context, c Config, reserve int64) bool {
	if ctx.Err() != nil || reserve < 0 || reserve > c.MaxStorageBytes || !filesystemStorageAvailable(c.DataDir, reserve) {
		return false
	}
	remaining := c.MaxStorageBytes - reserve
	var visit func(string, os.DirEntry) error
	visit = func(path string, entry os.DirEntry) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			err := scanStorageDirectory(ctx, path, visit)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.Type().IsRegular() {
			info, e := entry.Info()
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if info.Size() < 0 || info.Size() > remaining {
				return errStorageBudgetExceeded
			}
			remaining -= info.Size()
		}
		return nil
	}
	// Lstat preserves WalkDir's rule that symbolic links, including a linked
	// root, are not traversed. Files removed by concurrent cleanup are harmless.
	info, err := os.Lstat(c.DataDir)
	if err == nil {
		err = visit(c.DataDir, fs.FileInfoToDirEntry(info))
	}
	return ctx.Err() == nil && (err == nil || os.IsNotExist(err))
}

func waitForJobStorage(parent context.Context, c Config, s *Store, j *Job, token, work string) bool {
	if parent.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), workerDatabaseTimeout)
	defer cancel()
	if err := removeStorageTree(ctx, work); err != nil {
		return false
	}
	if err := s.WaitForStorage(ctx, j.ID, token, c); err != nil {
		return false
	}
	j.Status, j.Stage = "waiting_storage", "waiting_storage"
	j.Message = "Waiting for free storage"
	return true
}

func storagePressureError(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || exportProblem(err).code == "storage_full"
}
