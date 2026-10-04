package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLeaseWritesRejectExpiryWhileWaitingForDatabaseLock(t *testing.T) {
	for _, operation := range []string{"save", "heartbeat", "artifact"} {
		t.Run(operation, func(t *testing.T) {
			s, job, token, path, artifact := publicationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := s.DB.Exec(ctx, "UPDATE jobs SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1", job.ID); err != nil {
				t.Fatal(err)
			}
			lock, err := s.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(context.Background())
			if _, err := lock.Exec(ctx, "SELECT id FROM jobs WHERE id=$1 FOR UPDATE", job.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "save":
					job.Message = "stale worker write"
					done <- s.SaveJob(ctx, job, token)
				case "heartbeat":
					_, err := s.Heartbeat(ctx, job.ID, token)
					done <- err
				case "artifact":
					done <- s.AddArtifact(ctx, job.Owner, job.ID, path, token, artifact)
				}
			}()
			for {
				var waiting bool
				if err := lock.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks waiting JOIN pg_locks held
 ON held.locktype='transactionid' AND held.transactionid=waiting.transactionid AND held.granted
 WHERE NOT waiting.granted AND held.pid=pg_backend_pid())`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			for {
				var expired bool
				if err := lock.QueryRow(ctx, "SELECT lease_until<clock_timestamp() FROM jobs WHERE id=$1", job.ID).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := lock.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired worker %s was accepted: %v", operation, err)
			}
			current, err := s.Job(ctx, job.ID, job.Owner)
			if err != nil || current.Message == "stale worker write" {
				t.Fatalf("expired writer changed job state: %+v %v", current, err)
			}
		})
	}
}
