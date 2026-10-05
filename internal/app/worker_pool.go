package app

import (
	"context"
	"sync"
)

// The number of simultaneous encoders is fixed for this worker process. It
// never grows with queue depth. Callers own resource limits for the whole pool;
// FFmpeg's thread count remains a separate per-export setting.
func runWorkerPool(ctx context.Context, count int, run func()) {
	if count < 1 {
		count = 1
	}
	if count > 8 {
		count = 8
	}
	var workers sync.WaitGroup
	for range count {
		if ctx.Err() != nil {
			break
		}
		workers.Add(1)
		go func() { defer workers.Done(); run() }()
	}
	// All active process groups and their final lease/storage cleanup complete
	// before the shared database connection and health marker are closed.
	workers.Wait()
}
