package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerHealthRequiresSuccessfulAccessAndFreshPrivateMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-health")
	if CheckWorkerHealth(path) == nil {
		t.Fatal("missing marker appeared healthy")
	}
	health, err := newWorkerHealth(path)
	if err != nil {
		t.Fatal(err)
	}
	defer health.Close()
	if CheckWorkerHealth(path) == nil {
		t.Fatal("startup without queue access appeared healthy")
	}
	health.Touch()
	if err := CheckWorkerHealth(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatalf("health marker is not private and empty: %+v %v", info, err)
	}
	stale := time.Now().Add(-workerHealthMaxAge - time.Second)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	if CheckWorkerHealth(path) == nil {
		t.Fatal("stale queue access appeared healthy")
	}
	other, err := newWorkerHealth(filepath.Join(t.TempDir(), "other-container-health"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.Touch()
	if CheckWorkerHealth(path) == nil {
		t.Fatal("another worker masked an unhealthy marker")
	}
	health.Close()
	if CheckWorkerHealth(path) == nil {
		t.Fatal("stopped worker appeared healthy")
	}
}
