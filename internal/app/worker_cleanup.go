package app

import (
	"context"
	"errors"
	"log/slog"
)

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
