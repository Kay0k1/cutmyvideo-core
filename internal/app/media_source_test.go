package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLocalMediaDimensionsRefreshFencesLegacyDecoderCapacity(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	c.SourceTimeout = 20 * time.Second
	ctx := context.Background()
	path := filepath.Join(c.DataDir, "large-selected-video.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color=red:size=4096x2160:rate=1", "-t", "1", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", path); err != nil {
		t.Fatal(err)
	}
	// Historical inspection could persist the dimensions of a small cover.
	// This actual playable input exceeds the whole-pool decoder threshold.
	legacy := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Title: "Historical metadata", DurationMS: 12345, Path: path, Width: 512, Height: 512}
	if err := s.AddSource(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := s.DB.QueryRow(ctx, "SELECT (to_jsonb(s)-'width'-'height')::text FROM sources s WHERE id=$1", legacy.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.refreshLocalMediaDimensions(ctx, c, legacy)
	if err != nil || fresh.Width != 4096 || fresh.Height != 2160 || fresh.DurationMS != legacy.DurationMS {
		t.Fatalf("selected dimensions were not refreshed independently of duration: %+v %v", fresh, err)
	}
	persisted, err := s.Source(ctx, legacy.ID, legacy.Owner)
	if err != nil || persisted.Width != 4096 || persisted.Height != 2160 {
		t.Fatalf("active preview weight cannot see refreshed dimensions: %+v %v", persisted, err)
	}
	if err := s.DB.QueryRow(ctx, "SELECT (to_jsonb(s)-'width'-'height')::text FROM sources s WHERE id=$1", legacy.ID).Scan(&after); err != nil || after != before {
		t.Fatalf("refresh changed unrelated metadata: before=%s after=%s err=%v", before, after, err)
	}
	budget := withMediaBudget(ctx, 2)
	release, err := acquireMediaProcessing(budget, fresh, nil, ExportRequest{Format: "mp4"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	blocked, cancel := context.WithTimeout(budget, 30*time.Millisecond)
	defer cancel()
	if other, err := acquireMediaProcessing(blocked, Source{Path: "known-small", Width: 160, Height: 90}, nil, ExportRequest{Format: "mp4"}); !errors.Is(err, context.DeadlineExceeded) {
		if other != nil {
			other()
		}
		t.Fatalf("legacy large decoder left a second slot available: %v", err)
	}
	if cached, err := s.acquirePreview(ctx, c, fresh, newID("preview"), "large-window", false); err != nil || cached != nil {
		t.Fatalf("large preview admission: %v", err)
	}
	small := workerSourceFixture(t, s, c)
	if cached, err := s.acquirePreview(ctx, c, small, newID("preview"), "small-window", false); !errors.Is(err, errSourceServerBusy) {
		if cached != nil {
			cached.Close()
		}
		t.Fatalf("active preview join used obsolete cover dimensions: %v", err)
	}
	// Correctly inspected uploads do not acquire an unnecessary row version.
	var versionBefore, versionAfter string
	if err := s.DB.QueryRow(ctx, "SELECT xmin::text FROM sources WHERE id=$1", small.ID).Scan(&versionBefore); err != nil {
		t.Fatal(err)
	}
	if unchanged, err := s.refreshLocalMediaDimensions(ctx, c, small); err != nil || unchanged.Width != small.Width || unchanged.Height != small.Height {
		t.Fatalf("correct upload dimensions changed: %+v %v", unchanged, err)
	}
	if err := s.DB.QueryRow(ctx, "SELECT xmin::text FROM sources WHERE id=$1", small.ID).Scan(&versionAfter); err != nil || versionAfter != versionBefore {
		t.Fatalf("correct dimensions caused metadata churn: %s %s %v", versionBefore, versionAfter, err)
	}
}

func TestLocalMediaDimensionsRefreshPreservesIdentityAndCancellation(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	ctx := context.Background()
	source := workerSourceFixture(t, s, c)
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET width=12,height=12 WHERE id=$1", source.ID); err != nil {
		t.Fatal(err)
	}
	source.Width, source.Height = 12, 12
	wrongOwner := source
	wrongOwner.Owner = "another-owner"
	if _, err := s.refreshLocalMediaDimensions(ctx, c, wrongOwner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale owner altered source metadata: %v", err)
	}
	replacement := filepath.Join(c.DataDir, "replacement.mp4")
	bytes, err := os.ReadFile(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET path=$2 WHERE id=$1", source.ID, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := s.refreshLocalMediaDimensions(ctx, c, source); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale path altered replacement metadata: %v", err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET path=$2 WHERE id=$1", source.ID, source.Path); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT id FROM sources WHERE id=$1 FOR UPDATE", source.ID); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := s.refreshLocalMediaDimensions(bounded, c, source); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errDatabaseUnavailable) {
		t.Fatalf("caller deadline became database/unsupported-media failure: %v", err)
	}
	// pgx closes a canceled connection asynchronously. Keep the row fence
	// until the blocked statement is actually gone instead of racing its
	// cancellation with an unlock (which could allow a metadata-only commit).
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting int
		if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE l.relation='sources'::regclass AND a.wait_event_type='Lock' AND a.query LIKE 'UPDATE sources SET width=%' AND a.pid<>pg_backend_pid()").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled metadata update remained blocked behind its row fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	persisted, err := s.Source(ctx, source.ID, source.Owner)
	if err != nil || persisted.Width != 12 || persisted.Height != 12 || persisted.Path != source.Path {
		t.Fatalf("rejected refresh changed source: %+v %v", persisted, err)
	}
	observer, err := pgxpool.New(ctx, s.DB.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	s.DB.Close()
	if _, err := s.refreshLocalMediaDimensions(ctx, c, source); !errors.Is(err, errDatabaseUnavailable) {
		t.Fatalf("closed pool lost stable database-unavailable classification: %v", err)
	}
	var width, height int
	if err := observer.QueryRow(ctx, "SELECT width,height FROM sources WHERE id=$1", source.ID).Scan(&width, &height); err != nil || width != 12 || height != 12 {
		t.Fatalf("failed database refresh altered metadata: %dx%d %v", width, height, err)
	}
}

func TestLocalMediaDimensionsRefreshKeepsUnknownInputConservative(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	source.Width, source.Height = 160, 90
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET width=$2,height=$3 WHERE id=$1", source.ID, source.Width, source.Height); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.refreshLocalMediaDimensions(ctx, c, source)
	if err != nil || fresh.Width != 0 || fresh.Height != 0 {
		t.Fatalf("malformed media retained an unproven low decoder weight: %+v %v", fresh, err)
	}
	persisted, err := s.Source(ctx, source.ID, source.Owner)
	if err != nil || persisted.Width != 0 || persisted.Height != 0 {
		t.Fatalf("unknown dimensions were not shared with preview accounting: %+v %v", persisted, err)
	}
	missingTool := c
	missingTool.FFprobe = filepath.Join(c.DataDir, "absent-ffprobe")
	if _, err := s.refreshLocalMediaDimensions(ctx, missingTool, source); err == nil || unknownLocalMediaDimensions(err) {
		t.Fatalf("missing configured tool was hidden as malformed media: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.refreshLocalMediaDimensions(cancelled, c, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("early caller cancellation was hidden: %v", err)
	}
	if _, _, err := exportMedia(ctx, c, []string{source.Path}, false, Range{EndMS: 1000}, ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate"}, filepath.Join(c.DataDir, "invalid-result.mp4")); err == nil {
		t.Fatal("conservative admission bypassed actual invalid-media rejection")
	}
}
