package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"time"
)

func RunWorker(ctx context.Context, c Config, s *Store) error {
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
		processJob(ctx, c, s, job, token)
	}
}

func processJob(parent context.Context, c Config, s *Store, j Job, token string) {
	ctx, cancel := context.WithTimeout(parent, c.JobTimeout)
	defer cancel()
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
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
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(stopHeartbeat); <-heartbeatDone }()
	persist := func() bool {
		err := s.SaveJob(context.WithoutCancel(parent), j, token)
		if err != nil {
			slog.Error("job save failed", "job_id", j.ID, "error", err)
			cancel()
			return false
		}
		return true
	}
	finishFailure := func(message string) {
		j.Status = "failed"
		j.Stage = "finished"
		j.Message = message
		current, err := s.Job(context.WithoutCancel(parent), j.ID, j.Owner)
		if err == nil && current.Cancelled {
			j.Status = "cancelled"
			j.Message = "Cancelled"
		}
		for i := range j.Items {
			if j.Items[i].Status == "queued" || j.Items[i].Status == "running" {
				j.Items[i].Status = j.Status
				j.Items[i].Message = j.Message
			}
		}
		persist()
	}
	source, err := s.Source(ctx, j.Request.SourceID, j.Owner)
	if err != nil {
		finishFailure("Source is unavailable or has expired")
		return
	}
	inputs := []string{source.Path}
	remote := false
	var guard *networkGuard
	if source.Path == "" {
		j.Stage = "resolving"
		j.Message = "Resolving source streams"
		if !persist() {
			return
		}
		guard, err = newNetworkGuard(c.MaxSourceBytes)
		if err != nil {
			finishFailure("Could not prepare source access")
			return
		}
		defer guard.Close()
		resolveCtx, resolveCancel := context.WithTimeout(ctx, c.SourceTimeout)
		info, e := platformMetadata(resolveCtx, c, source.URL, guard)
		resolveCancel()
		if e != nil {
			finishFailure("Source streams are unavailable, require login, or timed out")
			return
		}
		if info.ID != source.ProviderID || math.Abs(info.Duration*1000-float64(source.DurationMS)) > 1000 {
			finishFailure("The source has changed; open it again before exporting")
			return
		}
		streams, e := pickStreams(info, j.Request.Quality, j.Request.Format)
		if e != nil {
			finishFailure("No supported seekable streams match these settings; upload the source instead")
			return
		}
		inputs = nil
		for _, f := range streams {
			u, e := guard.Relay(f.URL)
			if e != nil {
				finishFailure("A source stream uses an unsupported address")
				return
			}
			inputs = append(inputs, u)
		}
		remote = true
	}
	work := filepath.Join(c.DataDir, "work", j.ID+"-"+token)
	if err = os.MkdirAll(work, 0700); err != nil {
		finishFailure("Could not create a temporary workspace")
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
			finishFailure("Processing stopped or exceeded the time limit")
			return
		}
		item.Status = "running"
		item.Message = ""
		item.Artifact = nil
		j.Stage = "processing"
		j.Message = fmt.Sprintf("Processing fragment %d of %d", i+1, len(j.Items))
		if !persist() {
			return
		}
		if !storageAvailable(c, c.MaxOutputBytes) {
			finishFailure("Server storage is full; try again after older files expire")
			return
		}
		out := filepath.Join(work, item.ID+"."+j.Request.Format)
		start, end, e := exportMedia(ctx, c, inputs, remote, j.Request.Ranges[i], j.Request, out)
		if e != nil {
			if ctx.Err() != nil {
				finishFailure("Processing stopped or exceeded the time limit")
				return
			}
			item.Status = "failed"
			item.Message = "Could not export this fragment; try accurate mode, another quality, or an uploaded file"
			slog.Warn("fragment failed", "job_id", j.ID, "item_id", item.ID, "error", e)
			_ = os.Remove(out)
			if !persist() {
				return
			}
			continue
		}
		j.Stage = "publishing"
		j.Message = "Saving the fragment"
		if !persist() {
			return
		}
		id := newID("art")
		path := filepath.Join(c.DataDir, "artifacts", id+"."+j.Request.Format)
		if e = os.Rename(out, path); e != nil {
			item.Status = "failed"
			item.Message = "Could not save the result"
			if !persist() {
				return
			}
			continue
		}
		info, e := os.Stat(path)
		if e != nil {
			item.Status = "failed"
			item.Message = "Could not verify the result"
			if !persist() {
				return
			}
			continue
		}
		a := Artifact{ID: id, Filename: fmt.Sprintf("cut-%02d-%s.%s", i+1, item.ID[5:13], j.Request.Format), SizeBytes: info.Size(), DownloadURL: "/api/v1/artifacts/" + id + "/download", ActualStartMS: start, ActualEndMS: end}
		if e = s.AddArtifact(ctx, j.Owner, j.ID, path, token, a); e != nil {
			_ = os.Remove(path)
			item.Status = "failed"
			item.Message = "Could not register the result"
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
