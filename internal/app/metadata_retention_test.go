package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	type firstAttempt struct {
		cacheErr, mediaErr, parentErr error
		elapsed                       time.Duration
	}
	type cleanerResult struct {
		attempts, deferrals int
		err                 error
	}
	// Only the independently bounded cache stage may be deferred. Keep one
	// caller deadline across retries; media/DB errors and caller cancellation
	// must remain terminal rather than being mistaken for an empty cache.
	clean := func(ctx context.Context, s *Store, first chan<- firstAttempt, retryReady <-chan struct{}) cleanerResult {
		result := cleanerResult{}
		for result.attempts < 64 {
			result.attempts++
			started := time.Now()
			batch, err := s.cleanupStorageBatch(ctx, time.Hour, time.Hour)
			if result.attempts == 1 && first != nil {
				first <- firstAttempt{batch.cacheErr, err, ctx.Err(), time.Since(started)}
			}
			if err != nil || ctx.Err() != nil {
				result.err = errors.Join(ctx.Err(), batch.cacheErr, err)
				return result
			}
			if batch.cacheErr == nil {
				return result
			}
			var databaseErr *pgconn.PgError
			if !errors.Is(batch.cacheErr, context.DeadlineExceeded) || errors.Is(batch.cacheErr, context.Canceled) || errors.Is(batch.cacheErr, puddle.ErrClosedPool) || (errors.As(batch.cacheErr, &databaseErr) && databaseErr.Code != "57014") {
				result.err = batch.cacheErr
				return result
			}
			result.deferrals++
			if result.attempts == 1 && retryReady != nil {
				select {
				case <-retryReady:
				case <-ctx.Done():
					result.err = ctx.Err()
					return result
				}
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				result.err = ctx.Err()
				return result
			}
		}
		result.err = fmt.Errorf("cache deferral exceeded bounded retry attempts")
		return result
	}
	for _, delayed := range []bool{false, true} {
		t.Run(fmt.Sprintf("delayed_cache=%t", delayed), func(t *testing.T) {
			s := testStore(t)
			seedMetadataRetention(t, s, storageBatch*2, 19)
			before := metadataRetentionSnapshot(t, s.DB)
			setup, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelSetup()
			// Warm both of the fixture pool's connections. The separate locker
			// below does not consume either cleaner's pool-acquisition slot.
			if delayed {
				one, err := s.DB.Acquire(setup)
				if err != nil {
					t.Fatal(err)
				}
				two, err := s.DB.Acquire(setup)
				if err != nil {
					one.Release()
					t.Fatal(err)
				}
				_, oneErr := one.Exec(setup, "SELECT 1")
				_, twoErr := two.Exec(setup, "SELECT 1")
				one.Release()
				two.Release()
				if err := errors.Join(oneErr, twoErr); err != nil {
					t.Fatal(err)
				}
			}
			var lock pgx.Tx
			var retryReady chan struct{}
			if delayed {
				locker, err := pgx.ConnectConfig(setup, s.DB.Config().ConnConfig.Copy())
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					_ = locker.Close(cleanup)
				}()
				lock, err = locker.Begin(setup)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					_ = lock.Rollback(cleanup)
				}()
				if _, err := lock.Exec(setup, "LOCK TABLE source_metadata_cache IN ACCESS EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
				retryReady = make(chan struct{})
			}
			workerParent, cancelWorkers := context.WithCancel(context.Background())
			start := make(chan struct{})
			first := make(chan firstAttempt, 2)
			results := make(chan cleanerResult, 2)
			var group sync.WaitGroup
			defer func() { cancelWorkers(); group.Wait() }()
			for range 2 {
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					ctx, cancel := context.WithTimeout(workerParent, 5*time.Second)
					defer cancel()
					results <- clean(ctx, s, first, retryReady)
				}()
			}
			close(start)
			if delayed {
				// Observe a real relation-lock waiter; requiring two simultaneous
				// waiters inside a 250ms window would add another timing assumption.
				witness, cancel := context.WithTimeout(workerParent, time.Second)
				defer cancel()
				for {
					var waiting int
					if err := lock.QueryRow(witness, `SELECT count(*) FROM pg_locks WHERE relation='source_metadata_cache'::regclass AND NOT granted AND pid<>pg_backend_pid()`).Scan(&waiting); err != nil {
						t.Fatal("cache lock witness", err)
					}
					if waiting > 0 {
						t.Logf("controlled cache delay witnessed %d real ungranted table locks", waiting)
						break
					}
					select {
					case <-time.After(5 * time.Millisecond):
					case <-witness.Done():
						t.Fatal("cache operation never reached held PostgreSQL relation lock", witness.Err())
					}
				}
				for range 2 {
					select {
					case outcome := <-first:
						if outcome.mediaErr != nil || outcome.parentErr != nil || !errors.Is(outcome.cacheErr, context.DeadlineExceeded) {
							t.Fatalf("controlled first attempt must defer only cache with live caller: %+v", outcome)
						}
						t.Logf("first cache deferral elapsed=%s parent=%v media=%v", outcome.elapsed, outcome.parentErr, outcome.mediaErr)
					case <-setup.Done():
						t.Fatal("cleaners did not defer their blocked cache stages", setup.Err())
					}
				}
				// Client cancellation can precede the backend settling. Keep the
				// exclusive lock until actual canceled statements have disappeared,
				// so a late DELETE cannot run after we release the test barrier.
				settled, cancelSettled := context.WithTimeout(workerParent, time.Second)
				defer cancelSettled()
				settlingStarted := time.Now()
				for {
					var waiting int
					if err := lock.QueryRow(settled, `SELECT count(*) FROM pg_locks WHERE relation='source_metadata_cache'::regclass AND NOT granted AND pid<>pg_backend_pid()`).Scan(&waiting); err != nil {
						t.Fatal("canceled cache statement did not settle before unlock", err)
					}
					if waiting == 0 {
						break
					}
					select {
					case <-time.After(5 * time.Millisecond):
					case <-settled.Done():
						t.Fatal("canceled cache statement retained a relation-lock waiter", settled.Err())
					}
				}
				t.Logf("canceled cache statements settled before unlock in %s", time.Since(settlingStarted))
				var rows int
				if err := lock.QueryRow(settled, `SELECT count(*) FROM source_metadata_cache`).Scan(&rows); err != nil || rows != 419 {
					t.Fatalf("deferred statements changed cache while lock held: rows=%d error=%v", rows, err)
				}
				if err := lock.Rollback(settled); err != nil {
					t.Fatal(err)
				}
				close(retryReady)
			}
			group.Wait()
			close(results)
			for result := range results {
				if result.err != nil || (delayed && result.deferrals == 0) {
					t.Fatalf("concurrent cleaner did not recover within its original 5s caller: %+v", result)
				}
				t.Logf("cleaner success attempts=%d cache_deferrals=%d", result.attempts, result.deferrals)
			}
			if got := metadataRetentionSnapshot(t, s.DB); !reflect.DeepEqual(got, before[storageBatch*2:]) {
				t.Fatal("concurrent bounded cleaners failed to delete exactly 400 expired rows or changed 19 fresh identities/owners/payloads/deadlines")
			}
			assertion, cancelAssertion := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelAssertion()
			var sources int
			if err := s.DB.QueryRow(assertion, "SELECT count(*) FROM sources").Scan(&sources); err != nil || sources != 419 {
				t.Fatalf("cache cleaners changed source identity/count: count=%d error=%v", sources, err)
			}
		})
	}
	for _, failure := range []string{"caller_canceled", "database_error", "media_error"} {
		t.Run(failure, func(t *testing.T) {
			s := testStore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			switch failure {
			case "caller_canceled":
				cancel()
			case "database_error":
				if _, err := s.DB.Exec(ctx, "DROP TABLE source_metadata_cache"); err != nil {
					t.Fatal(err)
				}
			case "media_error":
				if _, err := s.DB.Exec(ctx, "DROP TABLE artifacts"); err != nil {
					t.Fatal(err)
				}
			}
			result := clean(ctx, s, nil, nil)
			if result.err == nil || result.attempts != 1 || result.deferrals != 0 {
				t.Fatalf("terminal error was hidden/retried: %+v", result)
			}
			if failure == "caller_canceled" {
				if !errors.Is(result.err, context.Canceled) {
					t.Fatal("caller cancellation lost", result.err)
				}
			} else {
				var databaseErr *pgconn.PgError
				if !errors.As(result.err, &databaseErr) || databaseErr.Code != "42P01" || ctx.Err() != nil {
					t.Fatalf("actual database/media failure lost or caller exhausted: %v parent=%v", result.err, ctx.Err())
				}
			}
		})
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

func TestMetadataRetentionWorkerDrainsMediaAfterCacheFailure(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	path := metadataRetentionArtifact(t, s, c)
	initialStored, initialReserved := ledgerBytes(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(ctx, "LOCK TABLE source_metadata_cache IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual worker cleanup orchestration while the cache remains
	// unavailable. Media must be durably removed before this lock is released.
	err = cleanupFiles(ctx, c, s)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "metadata cache retention") || ctx.Err() != nil {
		t.Fatalf("worker masked the cache cause or exhausted its whole budget: error=%v parent=%v", err, ctx.Err())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cache failure prevented physical artifact deletion", err)
	}
	var artifact, charged bool
	var stored, reserved int64
	if err := s.DB.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM artifacts WHERE id='cache-error-artifact'),
 EXISTS(SELECT 1 FROM storage_files WHERE path=$1),
 stored_bytes,reserved_bytes FROM storage_counters WHERE id=1`, path).Scan(&artifact, &charged, &stored, &reserved); err != nil {
		t.Fatal(err)
	}
	if artifact || charged || stored != initialStored-16 || reserved != initialReserved {
		t.Fatalf("worker retained/incorrectly released media charge after cache failure: artifact=%t charged=%t stored=%d want=%d reserved=%d want=%d", artifact, charged, stored, initialStored-16, reserved, initialReserved)
	}
	actualStored, actualReserved := ledgerBytes(t, s)
	if stored != actualStored || reserved != actualReserved {
		t.Fatalf("worker counters no longer match actual ledger: counter=%d/%d ledger=%d/%d", stored, reserved, actualStored, actualReserved)
	}
}
