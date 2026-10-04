package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
	cleanup := time.NewTicker(5 * time.Minute)
	defer cleanup.Stop()
	if err := s.Recover(ctx); err != nil {
		return err
	}
	cleanupFiles(ctx, c, s)
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-cleanup.C:
			if err := s.Recover(ctx); err != nil {
				slog.Error("recovery failed", "error", err)
			}
			cleanupFiles(ctx, c, s)
		default:
		}
		job, token, err := s.Claim(ctx)
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
				cancelled, err := s.Heartbeat(ctx, j.ID, token)
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
		resolveCtx, resolveCancel := context.WithTimeout(ctx, c.SourceTimeout)
		info, e := platformMetadata(resolveCtx, c, source.URL, guard)
		resolveCancel()
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil {
				finishFailure("source_timeout", "Source inspection timed out; open the video again or upload your file")
				return
			}
			problem := exportProblem(e)
			finishFailure(problem.code, problem.message)
			return
		}
		if info.ID != source.ProviderID || math.Abs(info.Duration*1000-float64(source.DurationMS)) > 1000 {
			finishFailure("source_changed", "The source has changed; open it again before exporting")
			return
		}
		streams, e = pickStreams(info, j.Request.Quality, j.Request.Format)
		if e != nil {
			problem := exportProblem(e)
			finishFailure(problem.code, problem.message)
			return
		}
		inputs = nil
		playlists = make([]hlsPlaylist, len(streams))
		for i, f := range streams {
			if isHLS(f) {
				playlists[i], e = loadHLS(ctx, guard, f, j.Request.Quality, 0)
				if e != nil {
					problem := exportProblem(e)
					finishFailure(problem.code, problem.message)
					return
				}
				if math.Abs(float64(playlists[i].DurationMS-source.DurationMS)) > 2000 {
					finishFailure("source_timeline_changed", "The recording is incomplete or its timeline changed; open a completed recording instead")
					return
				}
				inputs = append(inputs, mediaInput{})
			} else {
				u, e := guard.RelayWithHeaders(f.URL, f.Headers)
				if e != nil {
					finishFailure("unsupported_stream", "A source stream uses an unsupported address")
					return
				}
				inputs = append(inputs, mediaInput{Path: u, Remote: true})
			}
		}
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
		j.Stage = "processing"
		j.Message = fmt.Sprintf("Processing fragment %d of %d", i+1, len(j.Items))
		if !persist() {
			return
		}
		reserve := c.MaxOutputBytes
		for _, f := range streams {
			if isHLS(f) {
				reserve += 2 * c.MaxSourceBytes
				break
			}
		}
		if !storageAvailable(c, reserve) {
			finishFailure("storage_full", "Server storage is full; try again after older files expire")
			return
		}
		out := filepath.Join(work, item.ID+"."+j.Request.Format)
		itemInputs := append([]mediaInput(nil), inputs...)
		fragmentDir := filepath.Join(work, item.ID)
		var prepareErr error
		for k, f := range streams {
			if !isHLS(f) {
				continue
			}
			if prepareErr = os.MkdirAll(fragmentDir, 0700); prepareErr != nil {
				break
			}
			var path string
			var offset int64
			path, offset, prepareErr = stageHLS(ctx, c, guard, f, playlists[k], j.Request.Ranges[i], fragmentDir, k)
			if prepareErr != nil {
				break
			}
			itemInputs[k] = mediaInput{Path: path, OffsetMS: offset}
		}
		var start, end int64
		e := prepareErr
		if e == nil {
			start, end, e = exportInputs(ctx, c, itemInputs, j.Request.Ranges[i], j.Request, out)
		}
		_ = os.RemoveAll(fragmentDir)
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
		if e = s.AddArtifact(ctx, j.Owner, j.ID, path, token, a); e != nil {
			_ = os.Remove(path)
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
		item.Status = "succeeded"
		item.Artifact = &a
		if !persist() {
			return
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
		_ = os.Remove(path)
	}
	// Reconcile old files that survived a crash before database registration.
	// A generous floor avoids racing an active upload or artifact publication.
	for _, kind := range []string{"sources", "artifacts"} {
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
		entries, e := os.ReadDir(filepath.Join(c.DataDir, kind))
		if e != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			path := filepath.Join(c.DataDir, kind, entry.Name())
			if known[path] {
				continue
			}
			info, e := entry.Info()
			if e == nil && info.ModTime().Before(time.Now().Add(-ttl)) {
				_ = os.Remove(path)
			}
		}
	}
	// Remove abandoned temporary directories left by killed workers.
	entries, err := os.ReadDir(filepath.Join(c.DataDir, "work"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, e := entry.Info()
		if e == nil && info.ModTime().Before(time.Now().Add(-c.ArtifactTTL)) && info.ModTime().Before(time.Now().Add(-c.JobTimeout-time.Hour)) {
			_ = os.RemoveAll(filepath.Join(c.DataDir, "work", entry.Name()))
		}
	}
}

func storageAvailable(c Config, reserve int64) bool {
	var total int64
	err := filepath.WalkDir(c.DataDir, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, e := entry.Info()
			if e != nil {
				return e
			}
			total += info.Size()
		}
		return nil
	})
	return (err == nil || os.IsNotExist(err)) && total+reserve <= c.MaxStorageBytes
}
