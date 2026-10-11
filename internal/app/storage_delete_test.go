package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

func pendingDeletionPaths(t *testing.T, s *Store) []string {
	t.Helper()
	tx, err := s.storageTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	paths, err := pendingStoragePaths(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func addDeletionRows(t *testing.T, s *Store, paths []string, size int64) {
	t.Helper()
	if _, err := s.DB.Exec(context.Background(), `INSERT INTO storage_files(path,kind,size_bytes,delete_pending) SELECT path,'tombstone',$2,true FROM unnest($1::text[]) AS f(path)`, paths, size); err != nil {
		t.Fatal(err)
	}
}

func TestStorageDeletePoisonBatchDoesNotStarveAcrossCyclesAndRestart(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.DataDir = filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(c.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	s.ConfigureStorage(c)
	var poison, external []string
	for i := range storageBatch {
		path := filepath.Join(c.DataDir, fmt.Sprintf("000-poison-%03d", i))
		switch i % 3 {
		case 0:
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		case 1:
			// Keep the raw traversal string ahead of healthy names in SQL's
			// ordering. Its resolved target must remain completely untouched.
			path = c.DataDir + string(filepath.Separator) + ".." + string(filepath.Separator) + fmt.Sprintf("000-external-%03d", i)
			if err := os.WriteFile(path, []byte("external caller bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			external = append(external, path)
		case 2:
			if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			path = filepath.Join(path, "nested.media")
		}
		poison = append(poison, path)
	}
	addDeletionRows(t, s, poison, 17)
	var healthy []string
	for i := range storageBatch + 7 {
		healthy = append(healthy, writeLedgerFile(t, c, "sources", fmt.Sprintf("zzz-healthy-%03d", i), 19))
	}
	addDeletionRows(t, s, healthy, 19)
	first := pendingDeletionPaths(t, s)
	if len(first) != storageBatch {
		t.Fatal("fixture did not fill the first deletion batch", len(first))
	}
	for _, path := range first {
		if strings.Contains(path, "healthy") {
			t.Fatal("fixture did not put poison ahead of healthy files")
		}
	}
	ctx := context.Background()
	if err := cleanupFiles(ctx, c, s); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("poison files were not isolated as filesystem retries", err)
	}
	for _, path := range healthy {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("poison batch starved a later healthy file", path, err)
		}
	}
	for _, path := range external {
		if data, err := os.ReadFile(path); err != nil || string(data) != "external caller bytes" {
			t.Fatal("traversal tombstone removed caller bytes", err)
		}
	}
	var delayed int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM storage_files WHERE delete_pending AND delete_failures=1 AND delete_retry_at>clock_timestamp()").Scan(&delayed); err != nil || delayed != storageBatch {
		t.Fatal("poison retry/backoff was not persisted", delayed, err)
	}
	if stored, reserved := ledgerBytes(t, s); stored != storageBatch*17 || reserved != 0 {
		t.Fatal("poison bytes were released or healthy deletion stayed charged", stored, reserved)
	}
	// A new Store and pool contain no process-local retry state.
	restarted, err := OpenStore(ctx, s.DB.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.DB.Close)
	restarted.ConfigureStorage(c)
	later := writeLedgerFile(t, c, "sources", "zzz-after-restart", 23)
	addDeletionRows(t, restarted, []string{later}, 23)
	if err := cleanupFiles(ctx, c, restarted); err != nil {
		t.Fatal("restart retried delayed poison ahead of useful work", err)
	}
	if _, err := os.Stat(later); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("healthy file did not drain after restart", err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE storage_files SET delete_retry_at=clock_timestamp()-interval '1 second' WHERE delete_pending"); err != nil {
		t.Fatal(err)
	}
	if err := cleanupFiles(ctx, c, restarted); err == nil {
		t.Fatal("permanently invalid poison was accepted on retry")
	}
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM storage_files WHERE delete_pending AND delete_failures=2 AND delete_retry_at>clock_timestamp()+interval '50 seconds'").Scan(&delayed); err != nil || delayed != storageBatch {
		t.Fatal("retry did not back off after restart", delayed, err)
	}
	if stored, _ := ledgerBytes(t, restarted); stored != storageBatch*17 {
		t.Fatal("repeated poison retries changed retained charges", stored)
	}
}

func TestStorageDeleteDirectorySyncFailureKeepsChargeUntilDurableRetry(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	path := writeLedgerFile(t, c, "sources", "sync-before-release", 19)
	addDeletionRows(t, s, []string{path}, 19)
	ops := systemStorageDeleteOperations()
	ops.synchronize = func(directories ...string) error {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("directory synchronization preceded physical deletion", err)
		}
		if len(directories) != 1 || directories[0] != filepath.Dir(path) {
			t.Fatal("deletion did not synchronize its containing directory", directories)
		}
		return errors.Join(fsdurable.ErrSyncFailed, syscall.EIO)
	}
	err := s.drainStorageDeletes(context.Background(), c, []string{path}, ops)
	if !storageDeletionRetryOnly(err) || !errors.Is(err, syscall.EIO) {
		t.Fatal("failed deletion barrier did not schedule a filesystem retry", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 19 {
		t.Fatal("unsynchronized removal released storage charge", stored)
	}
	if err := s.DrainStorageDeletes(context.Background(), c, []string{path}); err != nil {
		t.Fatal(err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 19 {
		t.Fatal("stale supplied list bypassed persisted backoff", stored)
	}
	if _, err := s.DB.Exec(context.Background(), "UPDATE storage_files SET delete_retry_at=clock_timestamp() WHERE path=$1", path); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainStorageDeletes(context.Background(), c, []string{path}); err != nil {
		t.Fatal("already missing file could not complete its durable retry", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 0 {
		t.Fatal("durably acknowledged removal stayed charged", stored)
	}
}

func TestStorageDeleteLockAndCancellationFencePhysicalRemoval(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	path := writeLedgerFile(t, c, "sources", "locked", 19)
	addDeletionRows(t, s, []string{path}, 19)
	lock, err := s.storageTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	bounded, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = s.DrainStorageDeletes(bounded, c, []string{path})
	cancel()
	rollbackStorage(lock)
	if !errors.Is(err, context.DeadlineExceeded) || storageDeletionRetryOnly(err) {
		t.Fatal("database fence failure was treated as a filesystem retry", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 19 {
		t.Fatal("physical deletion bypassed the database fence", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	ops := systemStorageDeleteOperations()
	ops.synchronize = func(directories ...string) error {
		if err := fsdurable.SyncDirectories(directories...); err != nil {
			return err
		}
		stop()
		return nil
	}
	err = s.drainStorageDeletes(ctx, c, []string{path}, ops)
	stop()
	if !errors.Is(err, context.Canceled) || storageDeletionRetryOnly(err) {
		t.Fatal("cancellation before database acknowledgement was lost", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 19 {
		t.Fatal("canceled acknowledgement released storage charge", stored)
	}
	if err := s.DrainStorageDeletes(context.Background(), c, []string{path}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageDeleteStaleListCannotRemoveRegisteredReplacement(t *testing.T) {
	for _, scenario := range []string{"before-drain", "lost-ack-after-commit"} {
		t.Run(scenario, func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			path := writeLedgerFile(t, c, "sources", "reused-name", 19)
			addDeletionRows(t, s, []string{path}, 19)
			paths := pendingDeletionPaths(t, s)
			if scenario == "lost-ack-after-commit" {
				ops := systemStorageDeleteOperations()
				ops.commit = func(ctx context.Context, tx pgx.Tx) error {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
					return io.ErrUnexpectedEOF
				}
				if err := s.drainStorageDeletes(context.Background(), c, paths, ops); !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatal("fixture did not lose an actual COMMIT acknowledgement", err)
				}
				if stored, _ := ledgerBytes(t, s); stored != 0 {
					t.Fatal("committed durable deletion retained a charge", stored)
				}
			}
			replacement := []byte("new registered caller file")
			if err := os.WriteFile(path, replacement, 0600); err != nil {
				t.Fatal(err)
			}
			source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: path, DurationMS: 1000}
			if err := s.AddSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			if err := s.DrainStorageDeletes(context.Background(), c, paths); err != nil {
				t.Fatal("stale tombstone list was not harmless", err)
			}
			if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, replacement) {
				t.Fatal("stale tombstone removed the newly registered file", err)
			}
			if stored, _ := ledgerBytes(t, s); stored != int64(len(replacement)) {
				t.Fatal("registered replacement lost its charge", stored)
			}
		})
	}
}

func TestStorageDeleteSerializesConcurrentReplacementRegistration(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	path := writeLedgerFile(t, c, "sources", "reused-concurrently", 19)
	addDeletionRows(t, s, []string{path}, 19)
	removed, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ops := systemStorageDeleteOperations()
	ops.synchronize = func(directories ...string) error {
		close(removed)
		select {
		case <-release:
			return fsdurable.SyncDirectories(directories...)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	drained := make(chan error, 1)
	go func() { drained <- s.drainStorageDeletes(ctx, c, []string{path}, ops) }()
	select {
	case <-removed:
	case <-ctx.Done():
		t.Fatal("deletion did not reach the directory barrier")
	}
	replacement := []byte("complete concurrent replacement")
	if err := os.WriteFile(path, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	source := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: path, DurationMS: 1000}
	registered := make(chan error, 1)
	go func() { registered <- s.AddSource(ctx, source) }()
	select {
	case err := <-registered:
		t.Fatal("replacement registration bypassed the in-flight deletion fence", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-drained; err != nil {
		t.Fatal("fenced deletion failed", err)
	}
	if err := <-registered; err != nil {
		t.Fatal("replacement did not register after deletion committed", err)
	}
	// Another cleanup/API actor may still carry the original selected list.
	if err := s.DrainStorageDeletes(ctx, c, []string{path}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, replacement) {
		t.Fatal("concurrent replacement was removed by an old deletion", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != int64(len(replacement)) {
		t.Fatal("concurrent replacement was not charged exactly once", stored)
	}
}

func TestStorageDeletePreservesAnotherSourceSharingTheFile(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	path := writeLedgerFile(t, c, "sources", "shared.media", 19)
	first := Source{ID: newID("src"), Owner: "owner", Kind: "upload", Path: path, DurationMS: 1000}
	second := first
	second.ID = newID("src")
	ctx := context.Background()
	for _, source := range []Source{first, second} {
		if err := s.AddSource(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := s.DeleteSource(ctx, first.ID, first.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DrainStorageDeletes(ctx, c, paths); !storageDeletionRetryOnly(err) {
		t.Fatal("shared source path was not protected by authoritative references", err)
	}
	if _, err := s.Source(ctx, second.ID, second.Owner); err != nil {
		t.Fatal("other source disappeared during deletion", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 19 {
		t.Fatal("deletion removed another source's file", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 19 {
		t.Fatal("referenced file lost its charge", stored)
	}
	if _, err := s.DeleteSource(ctx, second.ID, second.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE storage_files SET delete_retry_at=clock_timestamp() WHERE path=$1", path); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainStorageDeletes(ctx, c, paths); err != nil {
		t.Fatal("unreferenced shared file did not complete deletion", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 0 {
		t.Fatal("unreferenced shared file remained charged", stored)
	}
}

func TestStorageDeleteBackoffIsBoundedAndRetombstonePreservesSchedule(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	path := filepath.Join(c.DataDir, "permanent-directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	addDeletionRows(t, s, []string{path}, 19)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, "UPDATE storage_files SET delete_failures=2147483647 WHERE path=$1", path); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainStorageDeletes(ctx, c, []string{path}); !storageDeletionRetryOnly(err) {
		t.Fatal("permanent directory did not remain retryable", err)
	}
	var count int
	var next time.Time
	var delay float64
	if err := s.DB.QueryRow(ctx, "SELECT delete_failures,delete_retry_at,extract(epoch FROM delete_retry_at-clock_timestamp()) FROM storage_files WHERE path=$1", path).Scan(&count, &next, &delay); err != nil || count != 2147483647 || delay < 3500 || delay > 3600 {
		t.Fatal("backoff overflowed or exceeded its one-hour cap", count, delay, err)
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	if err := tombstoneStorageFiles(ctx, tx, []storageDeletionFile{{path: path, owner: "owner", size: 19}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var retained time.Time
	if err := s.DB.QueryRow(ctx, "SELECT delete_retry_at FROM storage_files WHERE path=$1", path).Scan(&retained); err != nil || !retained.Equal(next) {
		t.Fatal("repeated tombstone creation bypassed poison backoff", retained, next, err)
	}
}

func TestStorageDeleteRejectsSymlinkedParentAndPersistsMissingHierarchy(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	external := t.TempDir()
	outside := filepath.Join(external, "caller.media")
	if err := os.WriteFile(outside, []byte("caller-owned bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.DataDir, "escape")
	if err := os.Symlink(external, link); err != nil {
		t.Skip("symlink fixture is unavailable: " + err.Error())
	}
	unsafe := filepath.Join(link, "caller.media")
	missing := filepath.Join(c.DataDir, "already-removed", "nested", "old.media")
	addDeletionRows(t, s, []string{unsafe, missing}, 19)
	if err := s.DrainStorageDeletes(context.Background(), c, []string{unsafe, missing}); !storageDeletionRetryOnly(err) {
		t.Fatal("symlink escape was not retained as a retry", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "caller-owned bytes" {
		t.Fatal("deletion traversed a symlink into caller storage", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 19 {
		t.Fatal("missing hierarchy was not durably acknowledged or escape was uncharged", stored)
	}
}

func TestStorageDeleteDuePickerUsesBoundedIndexBeforeFuturePoison(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,delete_pending,delete_retry_at) SELECT 'future-'||i,'tombstone',17,true,clock_timestamp()+interval '1 hour' FROM generate_series(1,5000) i;
INSERT INTO storage_files(path,kind,size_bytes,delete_pending) SELECT 'due-'||i,'tombstone',19,true FROM generate_series(1,201) i;`); err != nil {
		t.Fatal(err)
	}
	paths := pendingDeletionPaths(t, s)
	if len(paths) != storageBatch {
		t.Fatal("due picker was not bounded", len(paths))
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "due-") {
			t.Fatal("future poison blocked ready tombstones", path)
		}
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN (COSTS OFF) SELECT path FROM storage_files WHERE delete_pending AND delete_retry_at<=statement_timestamp() ORDER BY delete_retry_at,path LIMIT 200`)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !strings.Contains(strings.Join(plan, "\n"), "storage_files_delete_due_idx") || !strings.Contains(strings.Join(plan, "\n"), "Index Cond:") {
		t.Fatal("due picker cannot use the ordered partial retry index", plan, err)
	}
}
