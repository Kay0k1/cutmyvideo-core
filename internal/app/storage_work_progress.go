package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

const storageWorkAttempts = 64

func maintenanceStageContext(parent context.Context, stages int) (context.Context, context.CancelFunc) {
	budget := workerMaintenanceTimeout
	if deadline, ok := parent.Deadline(); ok {
		budget = max(0, time.Until(deadline))
	}
	return context.WithTimeout(parent, budget/time.Duration(stages))
}

func (s *Store) cleanupWorkProgress(ctx context.Context, c Config) error {
	var problems []error
	// Discovery has its own slice: a large or poisoned directory must not keep
	// already queued work from receiving a removal attempt this cycle.
	discovery, cancel := maintenanceStageContext(ctx, 2)
	session := newStorageScanSession()
	defer session.close()
	for attempt := 0; attempt < storageMaintenanceBatches; attempt++ {
		p, err := s.nextStorageScan(discovery, c, []string{"cleanup_work"}, attempt == 0)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			problems = append(problems, err)
			break
		}
		if err = s.storageScanStepSession(discovery, c, p, session); err != nil {
			problems = append(problems, err)
			break
		}
	}
	// Windows directory handles must be released before workspace removals.
	session.close()
	cancel()
	if ctx.Err() != nil {
		return errors.Join(append(problems, ctx.Err())...)
	}
	// Reservations whose workspace vanished are also queued. Retained/poisoned
	// candidates stay in the queue, allowing the next bounded insertion to reach
	// later reservations instead of selecting the same first LIMIT forever.
	tx, err := s.storageTx(ctx)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	_, err = tx.Exec(ctx, `INSERT INTO storage_work_cleanup(data_dir,path)
	 SELECT $1,$2||r.job_id||'-'||r.token FROM storage_reservations r WHERE r.kind='job'
	 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.id=r.job_id AND j.status='running' AND j.lease_token=r.token AND j.lease_until>statement_timestamp())
	 AND NOT EXISTS(SELECT 1 FROM storage_work_cleanup w WHERE w.data_dir=$1 AND w.path=$2||r.job_id||'-'||r.token)
	 ORDER BY r.expires_at,r.id LIMIT $3 ON CONFLICT DO NOTHING`, c.DataDir, filepath.Join(c.DataDir, "work")+string(os.PathSeparator), storageBatch)
	if err == nil {
		err = tx.Commit(ctx)
	}
	rollbackStorage(tx)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	// Select a bounded candidate list; each attempt commits its own scheduling
	// position immediately before I/O. Unattempted tail entries retain priority
	// when a short context stops the cycle midway through this list.
	tx, err = s.storageTx(ctx)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	rows, err := tx.Query(ctx, `SELECT path FROM storage_work_cleanup WHERE data_dir=$1 ORDER BY last_attempt_at,path LIMIT $2`, c.DataDir, storageWorkAttempts)
	var paths []string
	if err == nil {
		for rows.Next() {
			var path string
			if err = rows.Scan(&path); err != nil {
				break
			}
			paths = append(paths, path)
		}
		rows.Close()
		if err == nil {
			err = rows.Err()
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	rollbackStorage(tx)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	for i, path := range paths {
		if err = s.scheduleWorkAttempt(ctx, c, path); err != nil {
			problems = append(problems, err)
			break
		}
		// A single slow removal receives a fair share, leaving time for later
		// candidates. Partial tree removals themselves make durable progress.
		remaining := workerMaintenanceTimeout
		if deadline, ok := ctx.Deadline(); ok {
			remaining = max(0, time.Until(deadline))
		}
		attempt, stop := context.WithTimeout(ctx, min(250*time.Millisecond, max(20*time.Millisecond, remaining/time.Duration(len(paths)-i))))
		err = s.cleanupWorkPath(attempt, c, path)
		stop()
		if err != nil {
			problems = append(problems, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(problems...)
}

func (s *Store) scheduleWorkAttempt(ctx context.Context, c Config, path string) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, `UPDATE storage_work_cleanup SET last_attempt_at=clock_timestamp() WHERE data_dir=$1 AND path=$2`, c.DataDir, path); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) cleanupWorkPath(ctx context.Context, c Config, path string) error {
	root := filepath.Join(c.DataDir, "work")
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.ContainsAny(rel, `/\`) || filepath.IsAbs(rel) {
		return errors.New("refusing work path outside its root")
	}
	if err = safeScanPath(root, root); err != nil {
		return err
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE status='running' AND lease_until>clock_timestamp() AND id||'-'||lease_token=$1)`, rel).Scan(&active); err != nil || active {
		return err
	}
	var reservationID string
	var abandoned *time.Time
	err = tx.QueryRow(ctx, `UPDATE storage_reservations SET abandoned_at=COALESCE(abandoned_at,clock_timestamp()) WHERE kind='job' AND job_id||'-'||token=$1 RETURNING id,abandoned_at`, rel).Scan(&reservationID, &abandoned)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	info, statErr := os.Lstat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	staleLease := abandoned != nil && time.Since(*abandoned) > time.Minute
	cutoff := time.Now().Add(-max(c.ArtifactTTL, c.JobTimeout+time.Hour))
	if statErr == nil && !staleLease && !info.ModTime().Before(cutoff) {
		// Persist first abandonment; it is not evidence that files were removed.
		return tx.Commit(ctx)
	}
	if err = removeStorageTree(ctx, path); err != nil {
		return err
	}
	// Acknowledgement follows a directory barrier. On a crash or unknown COMMIT
	// outcome, either the queue/charge survives or the already durable removal
	// has been acknowledged; no retained file is freed merely by an attempt.
	parent := root
	if _, e := os.Lstat(parent); os.IsNotExist(e) {
		parent = c.DataDir
	}
	if err = fsdurable.SyncDirectories(parent); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM storage_files WHERE kind='work_orphan' AND (path=$1 OR starts_with(path,$2))`, path, path+string(os.PathSeparator)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM storage_work_cleanup WHERE data_dir=$1 AND path=$2`, c.DataDir, path); err != nil {
		return err
	}
	if reservationID != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM storage_reservations WHERE id=$1`, reservationID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
