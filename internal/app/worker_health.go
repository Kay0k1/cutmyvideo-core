package app

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

const workerHealthMaxAge = 30 * time.Second

type workerHealth struct{ path string }

func workerHealthPath(path string) string {
	if path == "" {
		return filepath.Join(os.TempDir(), "cutmy-worker-health")
	}
	return path
}

func newWorkerHealth(path string) (*workerHealth, error) {
	path = workerHealthPath(path)
	_ = os.Remove(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	// Creation alone is not evidence of a working queue/database.
	if err := os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &workerHealth{path: path}, nil
}

func (h *workerHealth) Touch() { touchWorkerHealth(h.path) }
func (h *workerHealth) Close() { _ = os.Remove(h.path) }

func touchWorkerHealth(path string) {
	now := time.Now()
	_ = os.Chtimes(workerHealthPath(path), now, now)
}

// The path belongs to this worker container's temporary filesystem, not the
// shared media volume. Freshness proves recent successful queue/lease access.
func CheckWorkerHealth(path string) error {
	info, err := os.Lstat(workerHealthPath(path))
	if err != nil {
		return errors.New("worker health is unavailable")
	}
	age := time.Since(info.ModTime())
	if !info.Mode().IsRegular() || age < 0 || age > workerHealthMaxAge {
		return errors.New("worker queue heartbeat is stale")
	}
	return nil
}
