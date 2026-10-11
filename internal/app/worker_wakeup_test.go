package app

import (
	"context"
	"testing"
	"time"
)

func TestWorkerNotificationWakesAfterCommitAndClosesOnCancellation(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := startWorkerWakeup(ctx, s)
	defer w.close()
	// The first broadcast confirms LISTEN was installed. snapshot can race
	// startup, so request a database notification until readiness is observed.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ready := w.snapshot()
		if _, err := s.DB.Exec(ctx, "SELECT pg_notify('cutmy_jobs','')"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ready:
			goto listening
		case <-time.After(50 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("listener did not become ready")
			}
		}
	}
listening:
	time.Sleep(20 * time.Millisecond) // Drain the readiness notification above.
	notified := w.snapshot()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_notify('cutmy_jobs','')"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
		t.Fatal("uncommitted submission woke the listener")
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("committed submission did not wake listener")
	}
	cancel()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not stop on cancellation")
	}
}

func TestInsertedJobEmitsQueueNotification(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	v := storedSource(t, s, "notification-owner")
	conn, err := s.DB.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "LISTEN cutmy_jobs"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "UNLISTEN cutmy_jobs")
	if _, err = s.CreateJob(ctx, v.Owner, requestFor(v), "notify-once"); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err = conn.Conn().WaitForNotification(waitCtx); err != nil {
		t.Fatalf("accepted job did not notify worker: %v", err)
	}
}
