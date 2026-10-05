package app

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	workerDatabaseTimeout    = 5 * time.Second
	workerMaintenanceTimeout = 10 * time.Second
	workerMaintenanceEvery   = 5 * time.Minute
)

var errStorageBudgetExceeded = errors.New("storage budget exceeded")

func claimWorkerJob(parent context.Context, s *Store) (Job, string, error) {
	ctx, cancel := context.WithTimeout(parent, workerDatabaseTimeout)
	defer cancel()
	return s.Claim(ctx)
}

func recoverWorkerJobs(parent context.Context, s *Store) error {
	ctx, cancel := context.WithTimeout(parent, workerDatabaseTimeout)
	defer cancel()
	return s.Recover(ctx)
}

func heartbeatWorkerJob(parent context.Context, s *Store, id, token string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, workerDatabaseTimeout)
	defer cancel()
	return s.Heartbeat(ctx, id, token)
}

// Maintenance must not stop queue claims or lease heartbeats while scanning
// retained media. Its success is not evidence of a healthy processing worker.
func startWorkerMaintenance(parent context.Context, c Config, s *Store) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		cycle := func(recover bool) {
			cycleCtx, cycleCancel := context.WithTimeout(ctx, workerMaintenanceTimeout)
			defer cycleCancel()
			if recover {
				if err := recoverWorkerJobs(cycleCtx, s); err != nil {
					slog.Error("recovery failed", "error", err)
				}
			}
			cleanupFiles(cycleCtx, c, s)
		}
		cycle(false)
		ticker := time.NewTicker(workerMaintenanceEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cycle(true)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// RunMaintenance is a worker-independent, one-shot cleanup entry point for an
// external scheduler. Its database waits and filesystem traversal share a
// bounded deadline, and concurrent invocations use the same storage lock.
func RunMaintenance(parent context.Context, c Config, s *Store) error {
	s.ConfigureStorage(c)
	ctx, cancel := context.WithTimeout(parent, workerMaintenanceTimeout)
	defer cancel()
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	if err = s.bootstrapStorage(ctx, tx, c); err == nil {
		err = tx.Commit(ctx)
	}
	rollbackStorage(tx)
	if err != nil {
		return err
	}
	if err = s.Recover(ctx); err != nil {
		return err
	}
	return cleanupFiles(ctx, c, s)
}
