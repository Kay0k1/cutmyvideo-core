package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPoolBoundsConcurrencyAndWaitsForShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{})
	var active, finished atomic.Int32
	go func() {
		runWorkerPool(ctx, 2, func() {
			active.Add(1)
			entered <- struct{}{}
			<-ctx.Done()
			<-release
			active.Add(-1)
			finished.Add(1)
		})
		close(done)
	}()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	if active.Load() != 2 {
		t.Fatal("unexpected concurrency", active.Load())
	}
	cancel()
	select {
	case <-done:
		t.Fatal("pool returned before process cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pool did not shut down")
	}
	if active.Load() != 0 || finished.Load() != 2 {
		t.Fatal("active work leaked")
	}
}

func TestWorkerPoolDoesNotStartAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runWorkerPool(ctx, 8, func() { t.Error("started after cancellation") })
}
