package app

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

func TestWorkerSlowFileSyncDoesNotConsumeRegistrationBudget(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout = 100<<20, 20*time.Second
	c.WorkerHealthPath = filepath.Join(c.DataDir, "health")
	ctx := context.Background()
	path := filepath.Join(c.DataDir, "source.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=800:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", path); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: newID("src"), Owner: "owner", Title: "Slow synchronization", Kind: "upload", Path: path, DurationMS: 4000}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	synchronizations := 0
	processJobWithPublisher(ctx, c, s, job, token, func(processingCtx context.Context, j Job, path, token string, a Artifact) error {
		return s.publishArtifact(processingCtx, j, path, token, a, func(path string, directories ...string) (fs.FileInfo, error) {
			synchronizations++
			info, err := fsdurable.Sync(path, directories...)
			if err != nil {
				return nil, err
			}
			// Model a filesystem barrier longer than the SQL budget. Starting
			// that budget in the worker expires it before registration begins.
			time.Sleep(workerDatabaseTimeout + 100*time.Millisecond)
			return info, nil
		})
	})
	finished, err := s.Job(ctx, job.ID, source.Owner)
	if err != nil || finished.Status != "succeeded" || finished.Items[0].Artifact == nil || synchronizations != 1 {
		t.Fatalf("slow synchronization prevented real artifact registration: %+v, syncs=%d, err=%v", finished, synchronizations, err)
	}
	output, _, err := s.ArtifactPath(ctx, finished.Items[0].Artifact.ID, source.Owner)
	if err != nil {
		t.Fatal("registered artifact cannot be downloaded", err)
	}
	media, _, err := probe(ctx, c, output, false)
	if err != nil || len(media.Streams) != 2 {
		t.Fatalf("registered artifact lost its media: %+v %v", media, err)
	}
}
