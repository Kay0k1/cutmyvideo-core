package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

func TestArtifactSyncFailureNeverRegistersSuccess(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	failure := errors.Join(fsdurable.ErrSyncFailed, syscall.EIO)
	err := s.publishArtifact(context.Background(), publicationSnapshot(before, a), path, token, a,
		func(string, ...string) (fs.FileInfo, error) { return nil, failure })
	assertPublicationError(t, err, false)
	if !errors.Is(err, failure) {
		t.Fatal("filesystem synchronization failure was lost", err)
	}
	assertPublicationRolledBack(t, s, before, a)
	var registered int
	if err := s.DB.QueryRow(context.Background(), "SELECT count(*) FROM storage_files WHERE path=$1", path).Scan(&registered); err != nil || registered != 0 {
		t.Fatal("failed synchronization registered file accounting", registered, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "result" {
		t.Fatal("Store removed a file whose ownership remains with its caller", err)
	}
}

func TestArtifactRegistrationRejectsUnsynchronizedSizeClaim(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	a.SizeBytes++
	err := s.PublishArtifact(context.Background(), publicationSnapshot(before, a), path, token, a)
	assertPublicationError(t, err, false)
	assertPublicationRolledBack(t, s, before, a)
}

func TestArtifactRegistrationRejectsMissingCompletedFile(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	err := s.PublishArtifact(context.Background(), publicationSnapshot(before, a), path, token, a)
	assertPublicationError(t, err, false)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing file cause was lost", err)
	}
	assertPublicationRolledBack(t, s, before, a)
}

func TestArtifactCancellationBeforeSyncNeverStartsPublication(t *testing.T) {
	s, before, token, path, a := publicationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.publishArtifact(ctx, publicationSnapshot(before, a), path, token, a,
		func(string, ...string) (fs.FileInfo, error) {
			t.Fatal("cancelled publication synchronized a file")
			return nil, nil
		})
	assertPublicationError(t, err, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation was lost", err)
	}
	assertPublicationRolledBack(t, s, before, a)
}
