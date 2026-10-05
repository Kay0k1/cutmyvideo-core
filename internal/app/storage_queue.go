package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"time"
)

func (s *Store) Claim(ctx context.Context) (Job, string, error) {
	c := s.storageConfig()
	if c.MaxStorageBytes <= 0 {
		return s.claimLegacy(ctx)
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return Job{}, "", err
	}
	defer rollbackStorage(tx)
	if err = s.bootstrapStorage(ctx, tx, c); err != nil {
		return Job{}, "", err
	}
	// A storage wait does not consume worker attempts. Its separate deadline is
	// finite even across restarts and retries, and includes periods without workers.
	if _, err = tx.Exec(ctx, `UPDATE jobs SET status='failed',stage='finished',message='Storage did not become available before the deadline',items=(SELECT jsonb_agg(CASE WHEN item->>'status' IN ('queued','running') THEN item||'{"status":"failed","message":"Storage wait timed out","error_code":"storage_timeout"}'::jsonb ELSE item END) FROM jsonb_array_elements(items) item),updated_at=now() WHERE status IN ('queued','waiting_storage') AND storage_wait_until<=clock_timestamp()`); err != nil {
		return Job{}, "", err
	}
	rows, err := tx.Query(ctx, `SELECT j.id,j.owner,s.kind IN ('platform','youtube') FROM jobs j JOIN sources s ON s.id=j.source_id LEFT JOIN queue_owners q ON q.owner=j.owner WHERE j.cancel_requested=false AND j.attempts<3 AND (j.status IN ('queued','waiting_storage') OR (j.status='running' AND j.lease_until<now())) ORDER BY q.last_considered ASC NULLS FIRST,q.last_claimed ASC NULLS FIRST,j.created_at,j.id LIMIT 128 FOR UPDATE OF j SKIP LOCKED`)
	if err != nil {
		return Job{}, "", err
	}
	type candidate struct {
		id, owner string
		platform  bool
	}
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.id, &v.owner, &v.platform); err != nil {
			rows.Close()
			return Job{}, "", err
		}
		candidates = append(candidates, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return Job{}, "", err
	}
	timeout := c.StorageWaitTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	for _, v := range candidates {
		if _, err = tx.Exec(ctx, `INSERT INTO queue_owners(owner,last_considered) VALUES($1,clock_timestamp()) ON CONFLICT(owner) DO UPDATE SET last_considered=excluded.last_considered`, v.owner); err != nil {
			return Job{}, "", err
		}
		j, err := readJob(ctx, tx, v.id, v.owner)
		if err != nil {
			return Job{}, "", err
		}
		// A vanished published file needs a fresh output budget on recovery;
		// metadata existence alone is insufficient to call a result complete.
		for i := range j.Items {
			item := &j.Items[i]
			if item.Status != "succeeded" || item.Artifact == nil {
				continue
			}
			var path string
			if err = tx.QueryRow(ctx, "SELECT path FROM artifacts WHERE id=$1 AND owner=$2", item.Artifact.ID, j.Owner).Scan(&path); err != nil {
				return Job{}, "", err
			}
			if _, err = os.Stat(path); os.IsNotExist(err) {
				item.Status = "queued"
				item.Artifact = nil
				item.ProgressMS = 0
			} else if err != nil {
				return Job{}, "", err
			}
		}
		reserve, err := remainingJobReserve(c, j, v.platform)
		if err != nil || reserve > c.MaxStorageBytes {
			message := "Export cannot fit in the configured storage budget"
			if _, err = tx.Exec(ctx, `UPDATE jobs SET status='failed',stage='finished',message=$2,items=$3::jsonb,updated_at=now() WHERE id=$1`, v.id, message, storageFailureItems(j, "storage_limit", message)); err != nil {
				return Job{}, "", err
			}
			continue
		}
		// A previous lease owns a separate reservation until its workspace is
		// actually removed. Reclaims never overwrite or uncharge old lease bytes.
		previousRows, e := tx.Query(ctx, "SELECT id,token FROM storage_reservations WHERE job_id=$1", j.ID)
		if e != nil {
			return Job{}, "", e
		}
		type oldLease struct{ id, token string }
		var old []oldLease
		for previousRows.Next() {
			var lease oldLease
			if e = previousRows.Scan(&lease.id, &lease.token); e != nil {
				previousRows.Close()
				return Job{}, "", e
			}
			old = append(old, lease)
		}
		previousRows.Close()
		if e = previousRows.Err(); e != nil {
			return Job{}, "", e
		}
		for _, lease := range old {
			work := filepath.Join(c.DataDir, "work", j.ID+"-"+lease.token)
			if _, e = os.Lstat(work); os.IsNotExist(e) {
				if _, e = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1", lease.id); e != nil {
					return Job{}, "", e
				}
			} else {
				if e != nil {
					return Job{}, "", e
				}
				if _, e = tx.Exec(ctx, "UPDATE storage_reservations SET abandoned_at=COALESCE(abandoned_at,clock_timestamp()) WHERE id=$1", lease.id); e != nil {
					return Job{}, "", e
				}
			}
		}
		fits, err := storageFits(ctx, tx, c, reserve, "")
		if err != nil {
			return Job{}, "", err
		}
		if !fits {
			if _, err = tx.Exec(ctx, `UPDATE jobs SET status='waiting_storage',stage='waiting_storage',message='Waiting for free storage',storage_wait_until=COALESCE(storage_wait_until,clock_timestamp()+($2*interval '1 second')),lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND (status IN ('queued','waiting_storage') OR (status='running' AND lease_until<clock_timestamp()))`, j.ID, timeout.Seconds()); err != nil {
				return Job{}, "", err
			}
			continue
		}
		token := newID("lease")
		if _, err = tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,token,job_id) VALUES($1,$2,'job',$3,clock_timestamp()+($4*interval '1 second'),$5,$6)`, jobReservationID(j.ID, token), j.Owner, reserve, (c.JobTimeout + time.Minute).Seconds(), token, j.ID); err != nil {
			return Job{}, "", err
		}
		items, _ := json.Marshal(j.Items)
		if _, err = tx.Exec(ctx, `UPDATE jobs SET status='running',stage='preparing',message='Preparing source',items=$3::jsonb,lease_until=clock_timestamp()+interval '45 seconds',lease_token=$2,attempts=attempts+1,updated_at=now() WHERE id=$1`, j.ID, token, items); err != nil {
			return Job{}, "", err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO queue_owners(owner,last_claimed,last_considered) VALUES($1,clock_timestamp(),clock_timestamp()) ON CONFLICT(owner) DO UPDATE SET last_claimed=excluded.last_claimed,last_considered=excluded.last_considered`, j.Owner); err != nil {
			return Job{}, "", err
		}
		j, err = readJob(ctx, tx, j.ID, j.Owner)
		if err != nil {
			return Job{}, "", err
		}
		if err = tx.Commit(ctx); err != nil {
			return Job{}, "", err
		}
		return j, token, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return Job{}, "", err
	}
	return Job{}, "", ErrNotFound
}

// Verify that the job still owns the persisted reservation before encoding. A
// physical disk can fill with external files even after logical admission.
func (s *Store) JobStorageAvailable(ctx context.Context, c Config, id, token string) (bool, error) {
	var reserved int64
	err := s.DB.QueryRow(ctx, `SELECT size_bytes FROM storage_reservations WHERE id=$1 AND token=$2`, jobReservationID(id, token), token).Scan(&reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Work already written by this job is part of its reservation. Checking only
	// the remaining physical safety floor avoids counting these bytes twice.
	return filesystemStorageAvailable(c.DataDir, c.StorageSafetyBytes), nil
}
