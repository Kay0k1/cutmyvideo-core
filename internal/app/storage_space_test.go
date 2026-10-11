//go:build linux || darwin || windows

package app

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageAdmissionChecksFilesystemSpaceIndependentlyOfLogicalCap(t *testing.T) {
	dir := t.TempDir()
	free, err := filesystemAvailableBytes(dir)
	if err != nil || free >= math.MaxInt64 {
		t.Fatal("could not measure the test filesystem", err)
	}
	c := Config{DataDir: dir, MaxStorageBytes: math.MaxInt64}
	// A moving filesystem can gain free bytes between measurement and
	// admission. This impossible request keeps the native observation useful
	// without assuming two snapshots are equal.
	if storageAvailable(c, math.MaxInt64) {
		t.Fatal("logical headroom permitted a reservation larger than filesystem free space")
	}
	if filesystemStorageAvailable(dir, -1) {
		t.Fatal("negative reservation was accepted")
	}
	if !storageAvailable(c, 1) {
		t.Fatal("small reservation on empty available storage was refused")
	}
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c.DataDir = filepath.Join(file, "data")
	if storageAvailable(c, 1) {
		t.Fatal("unreadable storage location must fail closed")
	}
}

func TestStorageAdmissionChecksNearestExistingFilesystem(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "new-данные", "media")
	free, err := filesystemAvailableBytes(missing)
	if err != nil || free == 0 || free >= math.MaxInt64 {
		t.Fatal("could not measure a missing data directory's existing ancestor", err)
	}
	if !filesystemStorageAvailable(missing, 1) || filesystemStorageAvailable(missing, math.MaxInt64) {
		t.Fatal("missing data directory did not retain its parent's physical admission bound")
	}
	if err := os.Mkdir(filepath.Dir(missing), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(missing, 0700); err != nil {
		t.Fatal(err)
	}
	if free, err = filesystemAvailableBytes(missing); err != nil || free == 0 {
		t.Fatal("could not measure the newly created native Unicode data directory", err)
	}
	if filesystemStorageAvailable(filepath.Join(dir, "invalid\x00path"), 1) {
		t.Fatal("an invalid native path must refuse admission")
	}
}

func TestStorageAdmissionIncludesOutstandingReservationsAndSafetyHeadroom(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// All required CI disks are smaller than this exabyte-scale reservation.
	// No files are allocated: the test exercises actual native free-space
	// queries and transactional admission instead of exhausting runner disks.
	const unavailable = int64(1 << 60)
	for _, name := range []string{"physical-space", "safety-headroom", "outstanding-reservations"} {
		t.Run(name, func(t *testing.T) {
			c := ledgerConfig(t)
			c.MaxStorageBytes, c.MaxOwnerBytes = math.MaxInt64, math.MaxInt64
			free, err := filesystemAvailableBytes(c.DataDir)
			if err != nil || free >= uint64(unavailable) {
				t.Fatal("could not establish the physical test-disk bound", err)
			}
			var existing int64
			switch name {
			case "physical-space":
				c.MaxSourceBytes = unavailable
			case "safety-headroom":
				c.StorageSafetyBytes = unavailable
			case "outstanding-reservations":
				existing = unavailable
				if _, err = s.DB.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at) VALUES('existing-worker','other-owner','job',$1,clock_timestamp()+interval '1 hour')`, existing); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.ReserveSource(ctx, c, "new-source"); !errors.Is(err, errSourceStorage) {
				t.Fatalf("physical headroom bypassed by %s: %v", name, err)
			}
			stored, reserved := ledgerBytes(t, s)
			if stored != 0 || reserved != existing {
				t.Fatalf("refused admission changed byte accounting: stored=%d reserved=%d", stored, reserved)
			}
			var count int
			if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM storage_reservations WHERE owner='new-source'").Scan(&count); err != nil || count != 0 {
				t.Fatal("refused admission retained a source reservation", count, err)
			}
			if existing != 0 {
				if _, err = s.DB.Exec(ctx, "DELETE FROM storage_reservations WHERE id='existing-worker'"); err != nil {
					t.Fatal(err)
				}
			}
			c.MaxSourceBytes, c.StorageSafetyBytes = 4<<20, 0
			if err = s.ReserveSource(ctx, c, "new-source"); err != nil {
				t.Fatal("normal admission failed after removing the physical headroom constraint", err)
			}
			if err = s.ReleaseSource(ctx, "new-source"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
