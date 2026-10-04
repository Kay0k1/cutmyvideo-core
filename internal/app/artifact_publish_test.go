package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func publicationFixture(t *testing.T) (*Store, Job, string, string, Artifact) {
	t.Helper()
	s := testStore(t)
	source := storedSource(t, s, "owner")
	ctx := context.Background()
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	j, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := Artifact{ID: newID("art"), Filename: "result.mp4", SizeBytes: 6, ActualStartMS: 1000, ActualEndMS: 3000}
	a.DownloadURL = "/api/v1/artifacts/" + a.ID + "/download"
	path := filepath.Join(t.TempDir(), a.ID+".mp4")
	if err := os.WriteFile(path, []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	return s, j, token, path, a
}

func publicationSnapshot(j Job, a Artifact) Job {
	j.Items = append([]JobItem(nil), j.Items...)
	j.Stage, j.Message = "publishing", "Saving the fragment"
	j.Items[0].Status, j.Items[0].Artifact = "succeeded", &a
	j.Items[0].ProgressMS = j.Items[0].EndMS - j.Items[0].StartMS
	return j
}

func assertPublicationError(t *testing.T, err error, uncertain bool) {
	t.Helper()
	var problem *ArtifactPublicationError
	if !errors.As(err, &problem) || problem.CommitUncertain != uncertain {
		t.Fatal("wrong publication outcome classification", err)
	}
}

func assertPublicationRolledBack(t *testing.T, s *Store, before Job, a Artifact) {
	t.Helper()
	ctx := context.Background()
	after, err := s.Job(ctx, before.ID, before.Owner)
	if err != nil || after.Stage != before.Stage || after.Status != before.Status || !reflect.DeepEqual(after.Items, before.Items) {
		t.Fatal("rejected publication changed the job snapshot", err)
	}
	if _, _, err := s.ArtifactPath(ctx, a.ID, before.Owner); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected publication left a registered artifact", err)
	}
}

func TestArtifactPublicationRollsBackInsertWhenJobWriteFails(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	// This failure happens after INSERT, inside the job snapshot write. The old
	// independent AddArtifact/SaveJob calls would leave the artifact registered.
	_, err := s.DB.Exec(context.Background(), `
CREATE FUNCTION reject_publication_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM artifacts WHERE job_id=NEW.id) THEN
  RAISE EXCEPTION 'publication update rejected' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER reject_publication_update BEFORE UPDATE ON jobs FOR EACH ROW
 WHEN (NEW.stage='publishing') EXECUTE FUNCTION reject_publication_update();`)
	if err != nil {
		t.Fatal(err)
	}
	err = s.PublishArtifact(context.Background(), publicationSnapshot(before, a), path, token, a)
	assertPublicationError(t, err, false)
	assertPublicationRolledBack(t, s, before, a)
}

func TestArtifactPublicationClassifiesDeferredCommitRejection(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	_, err := s.DB.Exec(context.Background(), `
CREATE FUNCTION reject_artifact_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'publication commit rejected' USING ERRCODE='23514'; END $$;
CREATE CONSTRAINT TRIGGER reject_artifact_commit AFTER INSERT ON artifacts
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_artifact_commit();`)
	if err != nil {
		t.Fatal(err)
	}
	err = s.PublishArtifact(context.Background(), publicationSnapshot(before, a), path, token, a)
	assertPublicationError(t, err, false)
	var constraint *pgconn.PgError
	if !errors.As(err, &constraint) || constraint.Code != "23514" {
		t.Fatal("deferred constraint failure was lost", err)
	}
	assertPublicationRolledBack(t, s, before, a)
}

func TestArtifactPublicationFencesStaleCancelledAndExpiredJobs(t *testing.T) {
	for _, scenario := range []string{"stale-token", "cancelled", "expired", "terminal", "other-owner"} {
		t.Run(scenario, func(t *testing.T) {
			s, before, token, path, a := publicationFixture(t)
			ctx := context.Background()
			snapshot := publicationSnapshot(before, a)
			var err error
			switch scenario {
			case "stale-token":
				token = "previous-lease"
			case "cancelled":
				err = s.Cancel(ctx, before.ID, before.Owner)
			case "expired":
				_, err = s.DB.Exec(ctx, "UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1", before.ID)
			case "terminal":
				_, err = s.DB.Exec(ctx, "UPDATE jobs SET status='failed' WHERE id=$1", before.ID)
			case "other-owner":
				snapshot.Owner = "other"
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err = s.Job(ctx, before.ID, before.Owner)
			if err != nil {
				t.Fatal(err)
			}
			err = s.PublishArtifact(ctx, snapshot, path, token, a)
			assertPublicationError(t, err, false)
			if !errors.Is(err, ErrNotFound) {
				t.Fatal("publication did not preserve fencing semantics", err)
			}
			assertPublicationRolledBack(t, s, before, a)
		})
	}
}

func TestArtifactPublicationConflictDoesNotReplaceExistingRegistration(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	ctx := context.Background()
	if err := s.AddArtifact(ctx, before.Owner, before.ID, path, token, a); err != nil {
		t.Fatal(err)
	}
	a.Filename = "overwrite.mp4"
	err := s.PublishArtifact(ctx, publicationSnapshot(before, a), path, token, a)
	assertPublicationError(t, err, false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("artifact conflict lost its existing rejection semantics", err)
	}
	loaded, err := s.Job(ctx, before.ID, before.Owner)
	if err != nil || !reflect.DeepEqual(loaded.Items, before.Items) {
		t.Fatal("conflicting artifact updated the job snapshot", err)
	}
	_, filename, err := s.ArtifactPath(ctx, a.ID, before.Owner)
	if err != nil || filename != "result.mp4" {
		t.Fatal("conflicting artifact replaced an existing registration", err)
	}
}

func publicationBarrier(t *testing.T, s *Store) (pgx.Tx, uint32) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, s.DB.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err = tx.Exec(ctx, "LOCK TABLE artifacts IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	var relation uint32
	if err = tx.QueryRow(ctx, "SELECT 'artifacts'::regclass::oid").Scan(&relation); err != nil {
		t.Fatal(err)
	}
	return tx, relation
}

func TestArtifactPublicationRechecksLeaseAfterBlockedInsert(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, "UPDATE jobs SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1", before.ID); err != nil {
		t.Fatal(err)
	}
	lock, relation := publicationBarrier(t, s)
	publicationCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- s.PublishArtifact(publicationCtx, publicationSnapshot(before, a), path, token, a)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("blocked publication did not stop")
		}
	})
	waitForCleanupLock(t, lock, relation)
	deadline, deadlineCancel := context.WithTimeout(ctx, 3*time.Second)
	defer deadlineCancel()
	for {
		var expired bool
		if err := lock.QueryRow(deadline, "SELECT lease_until<clock_timestamp() FROM jobs WHERE id=$1", before.ID).Scan(&expired); err != nil {
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
	select {
	case err := <-done:
		assertPublicationError(t, err, false)
		if !errors.Is(err, ErrNotFound) {
			t.Fatal("expired lease committed publication", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("released publication did not finish")
	}
	assertPublicationRolledBack(t, s, before, a)
}

func TestArtifactPublicationSerializesConcurrentCancellation(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	lock, relation := publicationBarrier(t, s)
	published := make(chan error, 1)
	go func() {
		published <- s.PublishArtifact(ctx, publicationSnapshot(before, a), path, token, a)
		close(published)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-published:
		case <-time.After(2 * time.Second):
			t.Error("publication did not stop")
		}
	})
	waitForCleanupLock(t, lock, relation)
	// A separate one-connection pool identifies the real Cancel query without
	// borrowing the publisher's connection or observing unrelated tests.
	config := s.DB.Config().Copy()
	config.MaxConns = 1
	cancelPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancelPool.Close)
	var cancelPID uint32
	if err := cancelPool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&cancelPID); err != nil {
		t.Fatal(err)
	}
	cancelStore := &Store{DB: cancelPool}
	cancelled := make(chan error, 1)
	go func() {
		cancelled <- cancelStore.Cancel(ctx, before.ID, before.Owner)
		close(cancelled)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-cancelled:
		case <-time.After(2 * time.Second):
			t.Error("cancellation did not stop")
		}
	})
	deadline, deadlineCancel := context.WithTimeout(ctx, 3*time.Second)
	defer deadlineCancel()
	for {
		var waiting bool
		if err := lock.QueryRow(deadline, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='transactionid' AND NOT granted)", cancelPID).Scan(&waiting); err != nil {
			t.Fatal("cancellation did not wait for atomic publication", err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, done := range []<-chan error{published, cancelled} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("publication/cancellation did not complete")
		}
	}
	loaded, err := s.Job(ctx, before.ID, before.Owner)
	if err != nil || !loaded.Cancelled || loaded.Items[0].Status != "succeeded" || loaded.Items[0].Artifact == nil || *loaded.Items[0].Artifact != a {
		t.Fatal("cancellation lost an already committed result", err)
	}
	loaded.Status, loaded.Stage = "succeeded", "finished"
	if err := s.SaveJob(ctx, loaded, token); err != nil {
		t.Fatal(err)
	}
	settled, err := s.Job(ctx, before.ID, before.Owner)
	if err != nil || settled.Status != "cancelled" || settled.Items[0].Status != "succeeded" || settled.Items[0].Artifact == nil || settled.Items[0].Artifact.ID != a.ID {
		t.Fatal("terminal cancellation changed the completed item", err)
	}
	if registered, _, err := s.ArtifactPath(ctx, a.ID, before.Owner); err != nil || registered != path {
		t.Fatal("committed result is unavailable after cancellation", err)
	}
}

type publicationLostAcknowledgement struct{ pgx.Tx }

func (tx publicationLostAcknowledgement) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func TestArtifactPublicationRecoveryUsesCommittedItemAfterLostAcknowledgement(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		name := "acknowledged"
		if uncertain {
			name = "lost-acknowledgement"
		}
		t.Run(name, func(t *testing.T) {
			s, before, token, path, a := publicationFixture(t)
			ctx := context.Background()
			snapshot := publicationSnapshot(before, a)
			if uncertain {
				tx, err := s.DB.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				err = publishArtifactTransaction(ctx, publicationLostAcknowledgement{tx}, snapshot, path, token, a)
				assertPublicationError(t, err, true)
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatal("lost commit acknowledgement was not preserved", err)
				}
			} else if err := s.PublishArtifact(ctx, snapshot, path, token, a); err != nil {
				t.Fatal(err)
			}
			registered, _, err := s.ArtifactPath(ctx, a.ID, before.Owner)
			if err != nil || registered != path {
				t.Fatal("committed artifact is unavailable", err)
			}
			if _, err := s.DB.Exec(ctx, "UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1", before.ID); err != nil {
				t.Fatal(err)
			}
			resumed, newToken, err := s.Claim(ctx)
			if err != nil || newToken == token || resumed.Items[0].Status != "succeeded" || resumed.Items[0].Artifact == nil || *resumed.Items[0].Artifact != a {
				t.Fatal("recovery lost the completed publication", err)
			}
			// A committed item must be skipped during recovery, even if an encoder
			// is unavailable. No duplicate row or missing download is acceptable.
			c := Config{DataDir: t.TempDir(), JobTimeout: time.Second, FFmpeg: "/does-not-exist", FFprobe: "/does-not-exist"}
			processJob(ctx, c, s, resumed, newToken)
			finished, err := s.Job(ctx, before.ID, before.Owner)
			if err != nil || finished.Status != "succeeded" || finished.Items[0].Artifact == nil || *finished.Items[0].Artifact != a {
				t.Fatal("recovery reprocessed or lost the committed result", err)
			}
			var count int
			if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE job_id=$1", before.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("recovery created a broken or duplicate registration", err)
			}
			if body, err := os.ReadFile(path); err != nil || string(body) != "result" {
				t.Fatal("committed result file was lost", err)
			}
		})
	}
}

func TestArtifactPublicationRejectsUnlinkedSnapshots(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	err := s.PublishArtifact(context.Background(), before, path, token, a)
	assertPublicationError(t, err, false)
	assertPublicationRolledBack(t, s, before, a)
}
