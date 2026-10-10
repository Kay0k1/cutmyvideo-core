package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/puddle/v2"
)

// Equal expired deadlines exercise the source_id tiebreaker; alternating owners
// and distinct payloads make accidental mutation of retained cache observable.
func seedMetadataRetention(t *testing.T, s *Store, expired, fresh int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,url,provider_id)
 SELECT 'cache-'||lpad(i::text,5,'0'),'owner-'||(i%2),'metadata fixture',10000,'platform','https://www.youtube.com/watch?v=fixture'||i,'fixture'||i
 FROM generate_series(1,$1::integer) i`, expired+fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO source_metadata_cache(source_id,owner,payload,expires_at)
 SELECT id,owner,convert_to('payload-'||id,'UTF8'),CASE WHEN substring(id from '[0-9]+$')::integer<=$1 THEN now()-interval '1 hour' ELSE now()+interval '1 day' END
 FROM sources`, expired); err != nil {
		t.Fatal(err)
	}
}

type retainedMetadataRow struct {
	id, owner, payload string
	expires            time.Time
}

func metadataRetentionSnapshot(t *testing.T, db *pgxpool.Pool) []retainedMetadataRow {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT source_id,owner,encode(payload,'hex'),expires_at FROM source_metadata_cache ORDER BY source_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []retainedMetadataRow
	for rows.Next() {
		var r retainedMetadataRow
		if err := rows.Scan(&r.id, &r.owner, &r.payload, &r.expires); err != nil {
			t.Fatal(err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMetadataRetentionBatchesPreserveLiveCacheAndIdentity(t *testing.T) {
	s := testStore(t)
	seedMetadataRetention(t, s, storageBatch*2+1, 23)
	before := metadataRetentionSnapshot(t, s.DB)
	ctx := context.Background()
	for cycle := 1; cycle <= 3; cycle++ {
		batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
		if err != nil || batch.cacheErr != nil {
			t.Fatalf("cycle %d: media=%v cache=%v", cycle, err, batch.cacheErr)
		}
		removed := min(cycle*storageBatch, storageBatch*2+1)
		after := metadataRetentionSnapshot(t, s.DB)
		if !reflect.DeepEqual(after, before[removed:]) {
			t.Fatalf("cycle %d did not remove exactly the oldest bounded cache batch or changed retained identity/payload/deadline", cycle)
		}
		if batch.full != (cycle < 3) || len(batch.paths) != 0 {
			t.Fatalf("cache-only backlog did not schedule bounded continuation: %+v", batch)
		}
	}
	var sources int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM sources").Scan(&sources); err != nil || sources != len(before) {
		t.Fatalf("cache retention changed its sources: count=%d error=%v", sources, err)
	}
}

func TestMetadataRetentionSkipsConcurrentRefreshLocks(t *testing.T) {
	s := testStore(t)
	seedMetadataRetention(t, s, storageBatch*2+7, 11)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	refresh, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer refresh.Rollback(context.Background())
	// A real pending refresh owns the oldest 200 rows. Their future deadlines
	// are uncommitted: maintenance must skip them and reach the healthy batch.
	if _, err := refresh.Exec(ctx, `UPDATE source_metadata_cache SET payload=convert_to('refreshed-'||source_id,'UTF8'),expires_at=now()+interval '1 day' WHERE source_id<=$1`, fmt.Sprintf("cache-%05d", storageBatch)); err != nil {
		t.Fatal(err)
	}
	batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
	if err != nil || batch.cacheErr != nil || !batch.full {
		t.Fatalf("locked prefix starved healthy expiry rows: batch=%+v error=%v", batch, err)
	}
	if err := refresh.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	beforeFinal := metadataRetentionSnapshot(t, s.DB)
	if len(beforeFinal) != storageBatch+7+11 {
		t.Fatal("maintenance did not delete exactly 200 rows after the locked prefix", len(beforeFinal))
	}
	for i, row := range beforeFinal[:storageBatch] {
		if row.id != fmt.Sprintf("cache-%05d", i+1) || row.payload != fmt.Sprintf("%x", "refreshed-"+row.id) || row.owner != fmt.Sprintf("owner-%d", (i+1)%2) || !row.expires.After(time.Now().Add(time.Hour)) {
			t.Fatal("concurrent refresh lost source/owner identity, payload or fresh deadline", row)
		}
	}
	batch, err = s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
	if err != nil || batch.cacheErr != nil || batch.full {
		t.Fatalf("final short expired batch: %+v, %v", batch, err)
	}
	want := append(slices.Clone(beforeFinal[:storageBatch]), beforeFinal[storageBatch+7:]...)
	if got := metadataRetentionSnapshot(t, s.DB); !reflect.DeepEqual(got, want) {
		t.Fatal("freshly refreshed or originally live cache was deleted/modified")
	}
}

func TestMetadataRetentionConcurrentCleanersPreserveFreshRows(t *testing.T) {
	s := testStore(t)
	seedMetadataRetention(t, s, storageBatch*2, 19)
	before := metadataRetentionSnapshot(t, s.DB)
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
			results <- errors.Join(batch.cacheErr, err)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := metadataRetentionSnapshot(t, s.DB); !reflect.DeepEqual(got, before[storageBatch*2:]) {
		t.Fatal("concurrent bounded cleaners failed to drain two batches or changed live cache")
	}
}

func metadataRetentionArtifact(t *testing.T, s *Store, c Config) string {
	t.Helper()
	s.ConfigureStorage(c)
	source := storedSource(t, s, "owner")
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO jobs(id,owner,source_id,request,items,status) VALUES('cache-error-job','owner',$1,'{}','[]','succeeded')`, source.ID); err != nil {
		t.Fatal(err)
	}
	path := writeLedgerFile(t, c, "artifacts", "cache-error-artifact", 16)
	if _, err := s.DB.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,expires_at)
 VALUES('cache-error-artifact','owner','cache-error-job',$1,'clip.mp4',16,0,1000,now()-interval '1 hour')`, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,owner,resource_id,kind,size_bytes) VALUES($1,'owner','cache-error-artifact','artifact',16)`, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMetadataRetentionCacheFailureKeepsCommittedMediaPaths(t *testing.T) {
	for _, public := range []bool{false, true} {
		t.Run(fmt.Sprintf("public=%t", public), func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			path := metadataRetentionArtifact(t, s, c)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			lock, err := s.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(context.Background())
			if _, err := lock.Exec(ctx, "LOCK TABLE source_metadata_cache IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			var paths []string
			if public {
				paths, err = s.Cleanup(ctx, time.Hour, time.Hour)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("public Cleanup hid the failed cache stage: %v", err)
				}
			} else {
				var batch storageCleanupBatch
				batch, err = s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
				paths = batch.paths
				if err != nil || !errors.Is(batch.cacheErr, context.DeadlineExceeded) {
					t.Fatalf("cache failure rolled back/masked successful media cleanup: batch=%+v error=%v", batch, err)
				}
			}
			if time.Since(start) > time.Second || ctx.Err() != nil || !slices.Equal(paths, []string{path}) {
				t.Fatalf("cache consumed the maintenance budget or discarded committed paths: elapsed=%s parent=%v paths=%v", time.Since(start), ctx.Err(), paths)
			}
			var artifact, pending bool
			if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts WHERE id='cache-error-artifact'),EXISTS(SELECT 1 FROM storage_files WHERE path=$1 AND delete_pending)`, path).Scan(&artifact, &pending); err != nil || artifact || !pending {
				t.Fatalf("media transaction did not commit independently: artifact=%t pending=%t error=%v", artifact, pending, err)
			}
			if err := lock.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.DrainStorageDeletes(ctx, c, paths); err != nil {
				t.Fatal("committed media paths could not be drained after cache error", err)
			}
		})
	}
}

func TestMetadataRetentionPreservesCancellationAndDatabaseErrors(t *testing.T) {
	s := testStore(t)
	seedMetadataRetention(t, s, 3, 2)
	before := metadataRetentionSnapshot(t, s.DB)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
	if !errors.Is(err, context.Canceled) || !errors.Is(batch.cacheErr, context.Canceled) || len(batch.paths) != 0 {
		t.Fatalf("caller cancellation was hidden or committed paths invented: batch=%+v error=%v", batch, err)
	}
	if got := metadataRetentionSnapshot(t, s.DB); !reflect.DeepEqual(got, before) {
		t.Fatal("canceled maintenance changed metadata cache")
	}
	s.DB.Close()
	batch, err = s.cleanupStorageBatch(context.Background(), time.Hour, time.Hour)
	if !errors.Is(err, puddle.ErrClosedPool) || !errors.Is(batch.cacheErr, puddle.ErrClosedPool) {
		t.Fatalf("database failure was silently treated as empty cache: batch=%+v error=%v", batch, err)
	}
}
