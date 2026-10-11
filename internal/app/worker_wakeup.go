package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Notifications shorten idle queue latency. Claims, leases and periodic
// polling remain authoritative, including across missed notifications and
// disconnected listeners. A dedicated connection leaves the bounded SQL pool
// available to active exports and maintenance.
type workerWakeup struct {
	mu     sync.Mutex
	next   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

func startWorkerWakeup(ctx context.Context, s *Store) *workerWakeup {
	ctx, cancel := context.WithCancel(ctx)
	w := &workerWakeup{next: make(chan struct{}), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for ctx.Err() == nil {
			connectCtx, stop := context.WithTimeout(ctx, workerDatabaseTimeout)
			conn, err := pgx.ConnectConfig(connectCtx, s.DB.Config().ConnConfig.Copy())
			if err == nil {
				_, err = conn.Exec(connectCtx, "LISTEN cutmy_jobs")
			}
			stop()
			if err == nil {
				// Also wake claims after reconnect, covering jobs accepted while
				// the listener could not receive notifications.
				w.signal()
				for ctx.Err() == nil {
					_, err = conn.WaitForNotification(ctx)
					if err != nil {
						break
					}
					w.signal()
				}
			}
			if conn != nil {
				closeCtx, stopClose := context.WithTimeout(context.Background(), time.Second)
				_ = conn.Close(closeCtx)
				stopClose()
			}
			if ctx.Err() != nil {
				return
			}
			// Do not log database exception text, which can contain a DSN.
			slog.Warn("queue notification listener unavailable; periodic claims remain enabled")
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return w
}

func (w *workerWakeup) signal() {
	w.mu.Lock()
	close(w.next)
	w.next = make(chan struct{})
	w.mu.Unlock()
}

func (w *workerWakeup) snapshot() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.next
}

func (w *workerWakeup) close() {
	w.cancel()
	<-w.done
}
