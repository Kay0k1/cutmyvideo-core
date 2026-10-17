package app

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
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
