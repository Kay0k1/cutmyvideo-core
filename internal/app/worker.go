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
	"time"
)

func RunWorker(ctx context.Context, c Config, s *Store) error {
	health, err := newWorkerHealth(c.WorkerHealthPath)
	if err != nil {
		return err
	}
	defer health.Close()
	if err := os.MkdirAll(filepath.Join(c.DataDir, "artifacts"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(c.DataDir, "work"), 0700); err != nil {
		return err
	}
	if err := recoverWorkerJobs(ctx, s); err != nil {
		return err
	}
	stopMaintenance := startWorkerMaintenance(ctx, c, s)
	defer stopMaintenance()
	for {
		if ctx.Err() != nil {
			return nil
		}
		job, token, err := claimWorkerJob(ctx, s)
		if errors.Is(err, ErrNotFound) {
			health.Touch()
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		if err != nil {
			slog.Error("job claim failed", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		health.Touch()
		processJob(ctx, c, s, job, token)
	}
}

func processJob(parent context.Context, c Config, s *Store, j Job, token string) {
	started := time.Now()
	defer func() {
		slog.Info("job_processing_stopped", "job_id", j.ID, "status", j.Status, "stage", j.Stage, "elapsed_ms", time.Since(started).Milliseconds())
	}()
	ctx, cancel := context.WithTimeout(parent, c.JobTimeout)
	defer cancel()
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	var leaseUncertain atomic.Bool
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				cancelled, err := heartbeatWorkerJob(ctx, s, j.ID, token)
				if cancelled || err != nil {
					if err != nil && ctx.Err() == nil {
						leaseUncertain.Store(true)
					}
					cancel()
					return
				}
				touchWorkerHealth(c.WorkerHealthPath)
			}
		}
	}()
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
		guard, err = newNetworkGuard(c.MaxSourceBytes)
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

	work := filepath.Join(c.DataDir, "work", j.ID+"-"+token)
	if err = os.MkdirAll(work, 0700); err != nil {
		finishFailure("server_error", "Could not create a temporary workspace")
		return
	}
	defer os.RemoveAll(work)
	for i := range j.Items {
		item := &j.Items[i]
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
		var e error
		var lastProgressSave time.Time
		report := func(ms int64) {
			if duration := item.EndMS - item.StartMS; ms > duration {
				ms = duration
			}
			item.ProgressMS = ms
			if time.Since(lastProgressSave) >= time.Second {
				lastProgressSave = time.Now()
				persist()
			}
		}
		for {
			reserve := c.MaxOutputBytes
			for _, f := range streams {
				if isHLS(f) {
					reserve += 2 * c.MaxSourceBytes
					break
				}
			}
			if !storageAvailableContext(ctx, c, reserve) {
				if ctx.Err() != nil {
					finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
				} else {
					finishFailure("storage_full", "Server storage is full; try again after older files expire")
				}
				return
			}
			itemInputs := append([]mediaInput(nil), inputs...)
			fragmentDir := filepath.Join(work, item.ID)
			e = nil
			for k, f := range streams {
				if !isHLS(f) {
					continue
				}
				if e = os.MkdirAll(fragmentDir, 0700); e != nil {
					break
				}
				var path string
				var offset int64
				path, offset, e = stageHLS(ctx, c, guard, f, playlists[k], j.Request.Ranges[i], fragmentDir, k)
				if e != nil {
					break
				}
				itemInputs[k] = mediaInput{Path: path, OffsetMS: offset}
			}
			if e == nil {
				start, end, e = exportInputsProgress(ctx, c, itemInputs, j.Request.Ranges[i], j.Request, out, report)
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
			j.Stage = "processing"
			j.Message = fmt.Sprintf("Processing fragment %d of %d", i+1, len(j.Items))
			if !persist() {
				return
			}
			slog.Info("cached_source_refreshed", "job_id", j.ID)
		}
		slog.Info("fragment_processed", "job_id", j.ID, "item_id", item.ID, "elapsed_ms", time.Since(encodeStarted).Milliseconds(), "media_ms", item.ProgressMS, "success", e == nil)
		if e != nil {
			if ctx.Err() != nil {
				finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
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
		if e = os.Rename(out, path); e != nil {
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
		info, e := os.Stat(path)
		if e != nil {
			_ = os.Remove(path)
			if ctx.Err() != nil || leaseUncertain.Load() {
				finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
				return
			}
			item.Status = "failed"
			item.Message = "Could not verify the result"
			item.ErrorCode = "server_error"
			if !persist() {
				return
			}
			continue
		}
		a := Artifact{ID: id, Filename: fmt.Sprintf("cut-%02d-%s.%s", i+1, item.ID[5:13], j.Request.Format), SizeBytes: info.Size(), DownloadURL: "/api/v1/artifacts/" + id + "/download", ActualStartMS: start, ActualEndMS: end}
		previous := *item
		item.Status = "succeeded"
		item.ProgressMS = item.EndMS - item.StartMS
		item.Artifact = &a
		publishCtx, publishCancel := context.WithTimeout(ctx, workerDatabaseTimeout)
		e = s.PublishArtifact(publishCtx, j, path, token, a)
		publishCancel()
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

func cleanupFiles(ctx context.Context, c Config, s *Store) {
	paths, err := s.Cleanup(ctx, c.ArtifactTTL, c.SourceTTL)
	if err != nil {
		slog.Error("cleanup failed", "error", err)
		return
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		_ = os.Remove(path)
	}
	// Reconcile old files that survived a crash before database registration.
	// A generous floor avoids racing an active upload or artifact publication.
	for _, kind := range []string{"sources", "artifacts"} {
		if ctx.Err() != nil {
			return
		}
		ttl := c.ArtifactTTL
		query := `SELECT path FROM artifacts`
		if kind == "sources" {
			ttl = c.SourceTTL
			query = `SELECT path FROM sources WHERE path<>'' UNION ALL SELECT thumbnail_path FROM sources WHERE thumbnail_path<>''`
		}
		floor := c.JobTimeout + c.SourceTimeout + time.Hour
		if ttl < floor {
			ttl = floor
		}
		rows, e := s.DB.Query(ctx, query)
		if e != nil {
			continue
		}
		known := map[string]bool{}
		valid := true
		for rows.Next() {
			var path string
			if e = rows.Scan(&path); e != nil {
				valid = false
				break
			}
			known[path] = true
		}
		if rows.Err() != nil {
			valid = false
		}
		rows.Close()
		if !valid {
			continue
		}
		cutoff := time.Now().Add(-ttl)
		_ = scanStorageDirectory(ctx, filepath.Join(c.DataDir, kind), func(path string, entry os.DirEntry) error {
			if !entry.Type().IsRegular() {
				return nil
			}
			if known[path] {
				return nil
			}
			info, e := entry.Info()
			if e == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(path)
			}
			return nil
		})
	}
	// Remove abandoned temporary directories left by killed workers.
	artifactCutoff, jobCutoff := time.Now().Add(-c.ArtifactTTL), time.Now().Add(-c.JobTimeout-time.Hour)
	_ = scanStorageDirectory(ctx, filepath.Join(c.DataDir, "work"), func(path string, entry os.DirEntry) error {
		info, e := entry.Info()
		if e == nil && info.ModTime().Before(artifactCutoff) && info.ModTime().Before(jobCutoff) {
			_ = os.RemoveAll(path)
		}
		return nil
	})
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
