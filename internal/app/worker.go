package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
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
	ctx = withMediaBudget(ctx, c.WorkerConcurrency)
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
	runWorkerPool(ctx, c.WorkerConcurrency, func() {
		for {
			if ctx.Err() != nil {
				return
			}
			job, token, err := claimWorkerJob(ctx, s)
			if errors.Is(err, ErrNotFound) {
				health.Touch()
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
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
	// An acknowledged/lost-ack publication may leave only the terminal
	// snapshot unsaved. Existing completed results need no new media resolution.
	completed := len(j.Items) > 0
	for _, item := range j.Items {
		if item.Status != "succeeded" || item.Artifact == nil {
			completed = false
			break
		}
		path, _, e := s.ArtifactPath(ctx, item.Artifact.ID, j.Owner)
		if e != nil {
			completed = false
			break
		}
		if _, e = os.Stat(path); e != nil {
			completed = false
			break
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

	var mediaPermit mediaProcessingPermit
	if budgetErr := mediaPermit.Acquire(ctx, source, streams, j.Request); budgetErr != nil {
		finishFailure("job_timeout", "Processing stopped or exceeded the time limit")
		return
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
			storageOK := true
			if c.MaxStorageBytes > 0 && s.storageConfig().MaxStorageBytes > 0 {
				var storageErr error
				storageOK, storageErr = s.JobStorageAvailable(ctx, c, j.ID, token)
				if storageErr != nil {
					finishFailure("server_error", "Could not check storage reservation")
					return
				}
			} else {
				reserve := outputBudget(c, j.Request.Ranges[i], j.Request)
				for _, f := range streams {
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
				j.Stage = "fetching"
				fragmentConfig := c
				fragmentConfig.MaxFetchBytes = inputBudget(c, j.Request.Ranges[i], j.Request)
				path, offset, e = stageHLSProgress(ctx, fragmentConfig, guard, f, playlists[k], j.Request.Ranges[i], fragmentDir, k, func(done, total int) {
					j.Message = fmt.Sprintf("Preparing fragment %d of %d: %d%% downloaded", i+1, len(j.Items), done*100/max(1, total))
					if time.Since(lastProgressSave) >= time.Second {
						lastProgressSave = time.Now()
						persist()
					}
				})
				if e != nil {
					break
				}
				itemInputs[k] = mediaInput{Path: path, OffsetMS: offset}
			}
			if e == nil {
				j.Stage = "processing"
				j.Message = fmt.Sprintf("Processing fragment %d of %d", i+1, len(j.Items))
				if !persist() {
					return
				}
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
			if e = mediaPermit.Acquire(ctx, source, streams, j.Request); e != nil {
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
		info, e := fsdurable.Publish(ctx, out, path)
		if e != nil {
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
		previous := *item
		item.Status = "succeeded"
		item.ProgressMS = item.EndMS - item.StartMS
		item.Artifact = &a
		e = publish(ctx, j, path, token, a)
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

func cleanupFiles(ctx context.Context, c Config, s *Store) error {
	if err := s.cleanupPreviews(ctx, c); err != nil {
		return err
	}
	var problems []error
	// Drain a busy expiry backlog within the existing maintenance deadline.
	// Every batch releases the shared storage lock for admissions/publications;
	// reconciliation still traverses the directories only once per cycle.
	for range 8 {
		batch, err := s.cleanupStorageBatch(ctx, c.ArtifactTTL, c.SourceTTL)
		if err != nil {
			slog.Error("cleanup failed", "error", err)
			return err
		}
		if err = s.DrainStorageDeletes(ctx, c, batch.paths); err != nil {
			slog.Warn("media deletion pending", "error", err)
			problems = append(problems, err)
			break
		}
		if !batch.full {
			break
		}
	}
	if err := s.ReconcileStorage(ctx, c); err != nil {
		slog.Error("storage reconciliation failed", "error", err)
		return errors.Join(append(problems, err)...)
	}
	// Pick up orphan tombstones created by reconciliation in the same cycle.
	tx, err := s.storageTx(ctx)
	if err == nil {
		var paths []string
		paths, err = pendingStoragePaths(ctx, tx)
		rollbackStorage(tx)
		if err == nil {
			if deletionErr := s.DrainStorageDeletes(ctx, c, paths); deletionErr != nil {
				problems = append(problems, deletionErr)
			}
		}
	}
	if err != nil {
		problems = append(problems, err)
	}
	cutoff := time.Now().Add(-max(c.ArtifactTTL, c.JobTimeout+time.Hour))
	scanErr := scanStorageDirectory(ctx, filepath.Join(c.DataDir, "work"), func(path string, entry os.DirEntry) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		info, e := entry.Info()
		if e != nil {
			return nil
		}
		var active bool
		if e = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE status='running' AND lease_until>clock_timestamp() AND id||'-'||lease_token=$1)`, entry.Name()).Scan(&active); e != nil {
			return e
		}
		if active {
			return nil
		}
		var reservationID string
		var abandoned *time.Time
		leaseTx, txErr := s.storageTx(ctx)
		if txErr != nil {
			return txErr
		}
		e = leaseTx.QueryRow(ctx, `UPDATE storage_reservations SET abandoned_at=COALESCE(abandoned_at,clock_timestamp()) WHERE kind='job' AND job_id||'-'||token=$1 RETURNING id,abandoned_at`, entry.Name()).Scan(&reservationID, &abandoned)
		if e == nil {
			e = leaseTx.Commit(ctx)
		}
		rollbackStorage(leaseTx)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		staleLease := abandoned != nil && time.Since(*abandoned) > time.Minute
		if !staleLease && !info.ModTime().Before(cutoff) {
			return nil
		}
		if e = removeStorageTree(ctx, path); e != nil {
			return e
		}
		// Bootstrap-accounted legacy work bytes remain charged until this
		// removal succeeds, including when no lease reservation exists.
		orphanTx, orphanErr := s.storageTx(ctx)
		if orphanErr != nil {
			return orphanErr
		}
		_, orphanErr = orphanTx.Exec(ctx, "DELETE FROM storage_files WHERE kind='work_orphan' AND starts_with(path,$1)", path+string(os.PathSeparator))
		if orphanErr == nil {
			orphanErr = orphanTx.Commit(ctx)
		}
		rollbackStorage(orphanTx)
		if orphanErr != nil {
			return orphanErr
		}
		if reservationID != "" {
			releaseTx, releaseErr := s.storageTx(ctx)
			if releaseErr != nil {
				return releaseErr
			}
			_, e = releaseTx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1", reservationID)
			if e == nil {
				e = releaseTx.Commit(ctx)
			}
			rollbackStorage(releaseTx)
		}
		return e
	})
	if scanErr != nil && !os.IsNotExist(scanErr) {
		problems = append(problems, scanErr)
	}
	// Missing workspaces prove that no retained temporary file needs its lease
	// reservation. Keep reservations for failed removals and still-active leases.
	rows, err := s.DB.Query(ctx, `SELECT r.job_id,r.token FROM storage_reservations r WHERE r.kind='job' AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.id=r.job_id AND j.status='running' AND j.lease_token=r.token AND j.lease_until>clock_timestamp()) LIMIT $1`, storageBatch)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	type reservation struct{ id, token string }
	var abandoned []reservation
	for rows.Next() {
		var r reservation
		if err = rows.Scan(&r.id, &r.token); err != nil {
			rows.Close()
			return err
		}
		abandoned = append(abandoned, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, r := range abandoned {
		path := filepath.Join(c.DataDir, "work", r.id+"-"+r.token)
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			if releaseErr := s.ReleaseJobStorage(ctx, r.id, r.token); releaseErr != nil {
				problems = append(problems, releaseErr)
			}
		}
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
