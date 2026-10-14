package app

import (
	"context"
	"sync"
)

// The number of simultaneous encoders is fixed for this worker process. It
// never grows with queue depth. Callers own resource limits for the whole pool;
// FFmpeg's thread count remains a separate per-export setting.
func runWorkerPool(ctx context.Context, count int, run func()) {
	runBoundedWorkerPool(ctx, count, 8, run)
}

func runWorkerPipelinePool(ctx context.Context, c Config, run func()) {
	// Additional admitted jobs prepare inputs while the existing encoder quota
	// remains enforced by the shared media budget. Neither grows with queue depth.
	runBoundedWorkerPool(ctx, max(1, min(c.WorkerConcurrency, 8))+max(0, min(c.WorkerPreparationConcurrency, 8)), 16, run)
}

func runBoundedWorkerPool(ctx context.Context, count, limit int, run func()) {
	if count < 1 {
		count = 1
	}
	if count > limit {
		count = limit
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
