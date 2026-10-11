package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type storageReadCounts struct {
	names, replay map[string]int
}

// Only the read batch choice sees this advancing mock clock. Its embedded
// context never expires: this test proves names/ACK/replay bounds, not a real
// five-millisecond filesystem or database completion guarantee.
type storageReadBudgetContext struct {
	context.Context
	remaining time.Duration
}

func (c storageReadBudgetContext) Deadline() (time.Time, bool) {
	return time.Now().Add(c.remaining), true
}

func TestStorageScanSessionDeadlineBatchesPreserveAcknowledgedPrefix(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		ctx       context.Context
		batchSize int
	}{
		{"short", storageReadBudgetContext{context.Background(), 5 * time.Millisecond}, 32},
		{"bounded", storageReadBudgetContext{context.Background(), 50 * time.Millisecond}, storageScanBatchSize},
		{"unbounded", context.Background(), storageRegistrationBatch},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c := ledgerConfig(t)
			const files = storageRegistrationBatch + 3
			storageSessionFiles(t, c, "sources", files)
			p := storageScanProgress{kind: "bootstrap_sources", path: scanRoot(c, "bootstrap_sources"), started: time.Now()}
			session := newStorageScanSession()
			defer func() { session.close() }()
			readNames, replayNames := 0, 0
			observe := func(read storageScanRead) {
				readNames += read.names
				if read.replay {
					replayNames += read.names
				}
			}
			session.observe = observe
			for batch := 0; ; batch++ {
				if batch >= files/scenario.batchSize+2 {
					t.Fatal("bounded reads did not reach EOF")
				}
				entries, digest, info, eof, reset, err := session.read(scenario.ctx, c, p)
				if err != nil || len(entries) > scenario.batchSize || info == nil {
					t.Fatal("read exceeded the chosen registration budget", len(entries), scenario.batchSize, err)
				}
				if batch == 0 && len(entries) != scenario.batchSize {
					t.Fatal("first read did not respect the deadline batch", len(entries), scenario.batchSize)
				}
				if reset {
					p.position = 0
				}
				p.position += int64(len(entries))
				p.digest = digest
				mtime, size := info.ModTime().UnixNano(), info.Size()
				p.mtime, p.size = &mtime, &size
				if eof {
					if p.position != files || len(session.readers) != 0 {
						t.Fatal("EOF skipped names or retained a directory", p.position, len(session.readers))
					}
					break
				}
				// This models acknowledgement only. Actual COMMIT, rollback and
				// unknown-outcome behavior remains covered by the PG tests.
				if !session.accept(p) {
					t.Fatal("acknowledged prefix could not be reused", p.position)
				}
				if batch == 0 {
					handle := session.readers[p.kind].dir
					session.close()
					assertStorageReaderClosed(t, handle)
					session = newStorageScanSession()
					session.observe = observe
				}
			}
			if replayNames != scenario.batchSize || readNames != files+scenario.batchSize {
				t.Fatal("restart replayed beyond the acknowledged prefix or omitted names", readNames, replayNames)
			}
		})
	}
}

func observeStorageReads(session *storageScanSession) *storageReadCounts {
	counts := &storageReadCounts{names: map[string]int{}, replay: map[string]int{}}
	session.observe = func(read storageScanRead) {
		counts.names[read.path] += read.names
		if read.replay {
			counts.replay[read.path] += read.names
		}
	}
	return counts
}

func storageSessionFiles(t *testing.T, c Config, folder string, count int) {
	t.Helper()
	for i := range count {
		writeLedgerFile(t, c, folder, fmt.Sprintf("entry-%05d", i), 1)
	}
}

func assertStorageReaderClosed(t *testing.T, dir *os.File) {
	t.Helper()
	// A second Close returns os.ErrClosed on all supported platforms. Stat on
	// a closed Windows directory instead reports ERROR_INVALID_HANDLE. An
	// actually open handle would close successfully here and fail this check.
	if err := dir.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("operation retained an open directory handle", err)
	}
}

func nextSessionStep(t *testing.T, s *Store, c Config, session *storageScanSession, kinds []string) storageScanProgress {
	t.Helper()
	p, err := s.nextStorageScan(context.Background(), c, kinds, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.storageScanStepSession(context.Background(), c, p, session); err != nil {
		t.Fatal(err)
	}
	return storageProgressRow(t, s, c, p.kind, p.path)
}

func TestStorageScanSessionReadsNamesLinearlyAcrossFairClasses(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	const perClass = storageRegistrationBatch*2 + 17
	for _, folder := range []string{"sources", "artifacts", "work"} {
		storageSessionFiles(t, c, folder, perClass)
	}
	session := newStorageScanSession()
	t.Cleanup(session.close)
	counts := observeStorageReads(session)
	firstSix := map[string]int{}
	var handles []*os.File
	maxHandles := 0
	for attempt := 0; ; attempt++ {
		p, err := s.nextStorageScan(context.Background(), c, bootstrapScanKinds, false)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil || attempt >= 20 {
			t.Fatal("fair scan did not complete", attempt, err)
		}
		if attempt < 6 {
			firstSix[p.kind]++
		}
		if err = s.storageScanStepSession(context.Background(), c, p, session); err != nil {
			t.Fatal(err)
		}
		maxHandles = max(maxHandles, len(session.readers))
		if len(session.readers) > storageScanReaders {
			t.Fatal("session exceeded the directory handle bound", len(session.readers))
		}
		for _, reader := range session.readers {
			handles = append(handles, reader.dir)
		}
	}
	for _, kind := range bootstrapScanKinds {
		path := scanRoot(c, kind)
		if firstSix[kind] != 2 || counts.names[path] != perClass || counts.replay[path] != 0 {
			t.Fatal("fair class replayed a committed prefix or skipped names", kind, firstSix, counts)
		}
	}
	if stored, reserved := ledgerBytes(t, s); stored != perClass*3 || reserved != 0 {
		t.Fatal("linear scan changed accounting", stored, reserved)
	}
	if len(session.readers) != 0 {
		t.Fatal("EOF retained a directory", len(session.readers))
	}
	for _, handle := range handles {
		assertStorageReaderClosed(t, handle)
	}
	t.Logf("three fair classes: names=%d replay=0, max handles=%d", perClass*3, maxHandles)
}

func TestStorageScanSessionRestartReplaysPrefixOnce(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	const files = storageRegistrationBatch*4 + 7
	storageSessionFiles(t, c, "sources", files)
	kinds := []string{"bootstrap_sources"}
	path := scanRoot(c, kinds[0])
	first := newStorageScanSession()
	t.Cleanup(first.close)
	firstCounts := observeStorageReads(first)
	nextSessionStep(t, s, c, first, kinds)
	p := nextSessionStep(t, s, c, first, kinds)
	if p.position != storageRegistrationBatch*2 || firstCounts.names[path] != int(p.position) || firstCounts.replay[path] != 0 {
		t.Fatal("first call repeated an acknowledged prefix", p.position, firstCounts)
	}
	oldHandle := first.readers[kinds[0]].dir
	first.close()
	assertStorageReaderClosed(t, oldHandle)
	restarted := storageProgressRestart(t, s)
	second := newStorageScanSession()
	t.Cleanup(second.close)
	counts := observeStorageReads(second)
	for range 3 {
		nextSessionStep(t, restarted, c, second, kinds)
	}
	if counts.replay[path] != storageRegistrationBatch*2 || counts.names[path] != files {
		t.Fatal("restart skipped the persisted prefix or replayed it every batch", counts)
	}
	if stored, _ := ledgerBytes(t, s); stored != files {
		t.Fatal("restart lost or double charged observations", stored)
	}
	if len(second.readers) != 0 {
		t.Fatal("completed restart retained a directory handle")
	}
	t.Logf("restart replay names=%d exactly once; second call total reads=%d", counts.replay[path], counts.names[path])
}

func TestStorageScanSessionDiscardsCanceledAndRacedBatches(t *testing.T) {
	for _, scenario := range []string{"canceled", "cursor-raced"} {
		t.Run(scenario, func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			const files = storageRegistrationBatch*2 + 7
			storageSessionFiles(t, c, "sources", files)
			kinds := []string{"bootstrap_sources"}
			session := newStorageScanSession()
			t.Cleanup(session.close)
			nextSessionStep(t, s, c, session, kinds)
			handle := session.readers[kinds[0]].dir
			p, err := s.nextStorageScan(context.Background(), c, kinds, false)
			if err != nil {
				t.Fatal(err)
			}
			wantPosition := int64(storageRegistrationBatch)
			if scenario == "canceled" {
				lock, err := s.storageTx(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer rollbackStorage(lock)
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
				err = s.storageScanStepSession(ctx, c, p, session)
				cancel()
				rollbackStorage(lock)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("blocked observation lost its deadline", err)
				}
			} else {
				if err = s.storageScanStep(context.Background(), c, p); err != nil {
					t.Fatal(err)
				}
				if err = s.storageScanStepSession(context.Background(), c, p, session); err != nil {
					t.Fatal("stale cursor did not yield harmlessly", err)
				}
				wantPosition *= 2
			}
			assertStorageReaderClosed(t, handle)
			if len(session.readers) != 0 {
				t.Fatal("unacknowledged read retained its live cursor")
			}
			got := storageProgressRow(t, s, c, p.kind, p.path)
			if got.position != wantPosition {
				t.Fatal("failed/raced observation advanced the durable cursor", got.position, wantPosition)
			}
			counts := observeStorageReads(session)
			path := p.path
			for attempts := 0; ; attempts++ {
				p, err = s.nextStorageScan(context.Background(), c, kinds, false)
				if errors.Is(err, pgx.ErrNoRows) {
					break
				}
				if err != nil || attempts > 3 {
					t.Fatal("recovery failed", err)
				}
				if err = s.storageScanStepSession(context.Background(), c, p, session); err != nil {
					t.Fatal(err)
				}
			}
			if counts.replay[path] != int(wantPosition) {
				t.Fatal("recovery trusted an unacknowledged OS cursor", counts.replay[path], wantPosition)
			}
			if stored, _ := ledgerBytes(t, s); stored != files {
				t.Fatal("recovery skipped or double charged files", stored)
			}
		})
	}
}

func TestStorageScanSessionDiscardsUnknownCommitOutcome(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed-%t", committed), func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			const files = storageRegistrationBatch*2 + 7
			storageSessionFiles(t, c, "sources", files)
			kinds := []string{"bootstrap_sources"}
			session := newStorageScanSession()
			t.Cleanup(session.close)
			nextSessionStep(t, s, c, session, kinds)
			handle := session.readers[kinds[0]].dir
			p, err := s.nextStorageScan(context.Background(), c, kinds, false)
			if err != nil {
				t.Fatal(err)
			}
			session.commit = func(ctx context.Context, tx pgx.Tx) error {
				if committed {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
				}
				return io.ErrUnexpectedEOF
			}
			if err = s.storageScanStepSession(context.Background(), c, p, session); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("fixture did not lose COMMIT acknowledgement", err)
			}
			assertStorageReaderClosed(t, handle)
			if len(session.readers) != 0 {
				t.Fatal("unknown outcome retained a speculative live cursor")
			}
			want := int64(storageRegistrationBatch)
			if committed {
				want *= 2
			}
			if got := storageProgressRow(t, s, c, p.kind, p.path); got.position != want {
				t.Fatal("fixture committed the wrong actual cursor", got.position, want)
			}
			session.commit = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
			counts := observeStorageReads(session)
			for attempts := 0; ; attempts++ {
				p, err = s.nextStorageScan(context.Background(), c, kinds, false)
				if errors.Is(err, pgx.ErrNoRows) {
					break
				}
				if err != nil || attempts > 3 {
					t.Fatal("unknown-outcome recovery failed", err)
				}
				if err = s.storageScanStepSession(context.Background(), c, p, session); err != nil {
					t.Fatal(err)
				}
			}
			if counts.replay[scanRoot(c, kinds[0])] != int(want) {
				t.Fatal("lost acknowledgement was reused without reading committed SQL progress", counts)
			}
			if stored, _ := ledgerBytes(t, s); stored != files {
				t.Fatal("unknown-outcome recovery skipped or double charged files", stored)
			}
		})
	}
}

func TestStorageScanSessionMembershipChangeRestartsCoverage(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	const files = storageRegistrationBatch*2 + 7
	storageSessionFiles(t, c, "sources", files)
	kinds := []string{"bootstrap_sources"}
	session := newStorageScanSession()
	t.Cleanup(session.close)
	before := nextSessionStep(t, s, c, session, kinds)
	handle := session.readers[kinds[0]].dir
	writeLedgerFile(t, c, "sources", "late-membership", 11)
	after := nextSessionStep(t, s, c, session, kinds)
	assertStorageReaderClosed(t, handle)
	if after.position != storageRegistrationBatch || !after.started.After(before.started) {
		t.Fatal("changed membership reused old coverage", before.position, after.position, before.started, after.started)
	}
	for range 2 {
		nextSessionStep(t, s, c, session, kinds)
	}
	if stored, _ := ledgerBytes(t, s); stored != files+11 {
		t.Fatal("new generation skipped a late file or double charged old files", stored)
	}
}

func TestStorageScanSessionRejectsStaleDirectoryIdentity(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	storageSessionFiles(t, c, "sources", storageRegistrationBatch*2+7)
	kinds := []string{"bootstrap_sources"}
	session := newStorageScanSession()
	t.Cleanup(session.close)
	p := nextSessionStep(t, s, c, session, kinds)
	reader := session.readers[p.kind]
	oldHandle := reader.dir
	// Inject the state of a stale open directory after path replacement. Native
	// Windows may itself prohibit replacing an open directory, so exercise the
	// SameFile guard with a real different inode/identity on every platform.
	other := t.TempDir()
	foreign, err := os.Open(other)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { foreign.Close() })
	identity, err := foreign.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader.dir.Close()
	reader.dir, reader.identity = foreign, identity
	counts := observeStorageReads(session)
	nextSessionStep(t, s, c, session, kinds)
	assertStorageReaderClosed(t, oldHandle)
	assertStorageReaderClosed(t, foreign)
	if counts.replay[p.path] != storageRegistrationBatch {
		t.Fatal("same durable tuple trusted another directory's handle", counts)
	}
	current, err := os.Stat(p.path)
	if err != nil || !os.SameFile(session.readers[p.kind].identity, current) {
		t.Fatal("reopened handle did not identify the configured directory", err)
	}
	session.close()
	if err := os.RemoveAll(filepath.Join(c.DataDir, "sources")); err != nil {
		t.Fatal("closed session prevented directory removal", err)
	}
}

func TestStorageScanSessionWorkPathSwitchClosesReader(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	const perDirectory = storageRegistrationBatch + 7
	storageSessionFiles(t, c, filepath.Join("work", "workspace-a"), perDirectory)
	storageSessionFiles(t, c, filepath.Join("work", "workspace-b"), perDirectory)
	session := newStorageScanSession()
	t.Cleanup(session.close)
	counts := observeStorageReads(session)
	kinds := []string{"bootstrap_work"}
	nextSessionStep(t, s, c, session, kinds) // Discover both descendants.
	a := nextSessionStep(t, s, c, session, kinds)
	handle := session.readers[a.kind].dir
	b := nextSessionStep(t, s, c, session, kinds)
	if a.path == b.path || len(session.readers) != 1 {
		t.Fatal("work scheduling did not exercise a bounded reader path switch", a.path, b.path)
	}
	assertStorageReaderClosed(t, handle)
	nextSessionStep(t, s, c, session, kinds)
	if counts.replay[a.path] != storageRegistrationBatch {
		t.Fatal("revisiting an evicted descendant skipped its verified prefix", counts.replay[a.path])
	}
	nextSessionStep(t, s, c, session, kinds)
	if stored, _ := ledgerBytes(t, s); stored != perDirectory*2 {
		t.Fatal("switching work paths lost or double charged observations", stored)
	}
}

func TestStorageScanSessionPartialOperationsReleaseDirectories(t *testing.T) {
	for _, operation := range []string{"bootstrap", "reconcile"} {
		t.Run(operation, func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			files := storageRegistrationBatch*2 + 7
			if operation == "bootstrap" {
				// The two absent classes consume one scheduling step each. The
				// source class must still be unfinished at the 24-step boundary.
				files = storageRegistrationBatch*storageProgressBatches + 7
			}
			storageSessionFiles(t, c, "sources", files)
			kind := "reconcile_sources"
			if operation == "bootstrap" {
				kind = "bootstrap_sources"
				if err := s.bootstrapStorage(context.Background(), c); !errors.Is(err, ErrStorageInitializing) {
					t.Fatal("large bootstrap did not return at its public-call budget", err)
				}
			} else if err := s.reconcileStorageProgress(context.Background(), c, 2); err != nil {
				t.Fatal(err)
			}
			path := scanRoot(c, kind)
			p := storageProgressRow(t, s, c, kind, path)
			if p.position == 0 || p.position >= int64(files) {
				t.Fatal("fixture did not return with a partial live reader", p.position, files)
			}
			// Windows rejects this when os.Open's directory handle survives the
			// operation. This also tests the non-EOF warmup/maintenance exits.
			moved := path + "-after-call"
			if err := os.Rename(path, moved); err != nil {
				t.Fatal("partial operation retained its directory handle", err)
			}
			if err := os.RemoveAll(moved); err != nil {
				t.Fatal("partial operation prevented directory removal", err)
			}
			t.Logf("%s returned with committed cursor=%d/%d; directory rename/removal succeeded", operation, p.position, files)
		})
	}
}

func TestStorageScanSessionReadBatchPreservesNonISODateStyle(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	const files = storageRegistrationBatch*2 + 7
	storageSessionFiles(t, c, "sources", files)
	kinds := []string{"bootstrap_sources"}
	first := newStorageScanSession()
	t.Cleanup(first.close)
	p := nextSessionStep(t, s, c, first, kinds)
	first.close()
	if p.position != storageRegistrationBatch {
		t.Fatal("fixture did not establish an acknowledged prefix", p.position)
	}
	fixed := time.Date(2003, time.November, 19, 12, 13, 14, 987654000, time.UTC)
	if _, err := s.DB.Exec(ctx, `UPDATE storage_scan_progress SET started_at=$4 WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path, fixed); err != nil {
		t.Fatal(err)
	}
	p = storageProgressRow(t, s, c, p.kind, p.path)
	config := s.DB.Config()
	config.ConnConfig.RuntimeParams["datestyle"] = "SQL, DMY"
	config.MaxConns = 1
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	localized := &Store{DB: db}
	var effective, rendered string
	if err = db.QueryRow(ctx, `SELECT current_setting('DateStyle'),started_at::text FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path).Scan(&effective, &rendered); err != nil {
		t.Fatal(err)
	}
	if effective != "SQL, DMY" || !strings.Contains(rendered, "19/11/2003") {
		t.Fatal("fixture did not establish effective non-ISO timestamp results", effective, rendered)
	}
	read := storageProgressRow(t, localized, c, p.kind, p.path)
	if read.position != p.position || read.digest != p.digest || !read.started.Equal(fixed) {
		t.Fatal("localized pool changed the acknowledged cursor before observation", read)
	}
	session := newStorageScanSession()
	t.Cleanup(session.close)
	if err = localized.storageScanStepSession(ctx, c, p, session); err != nil {
		t.Fatal("batched cursor could not decode non-ISO DateStyle", err)
	}
	partial := storageProgressRow(t, localized, c, p.kind, p.path)
	if partial.position != storageRegistrationBatch*2 || !partial.started.Equal(fixed) || partial.digest == p.digest {
		t.Fatal("batched read changed the exact start timestamp or acknowledged prefix", partial)
	}
	if _, err = localized.nextStorageScan(ctx, c, kinds, false); err != nil {
		t.Fatal(err)
	}
	if err = localized.storageScanStepSession(ctx, c, partial, session); err != nil {
		t.Fatal(err)
	}
	var started, completed time.Time
	var position int64
	if err = db.QueryRow(ctx, `SELECT position,started_at,completed_at FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path).Scan(&position, &started, &completed); err != nil {
		t.Fatal("localized pool could not read completed coverage", err)
	}
	var originalStarted, originalCompleted time.Time
	if err = s.DB.QueryRow(ctx, `SELECT started_at,completed_at FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, p.kind, p.path).Scan(&originalStarted, &originalCompleted); err != nil {
		t.Fatal(err)
	}
	if position != files || !started.Equal(fixed) || !started.Equal(originalStarted) || !completed.Equal(originalCompleted) || !completed.After(started) || len(session.readers) != 0 {
		t.Fatal("DateStyle changed durable timestamps, cursor or completed coverage", position, started, completed)
	}
	if stored, reserved := ledgerBytes(t, s); stored != files || reserved != 0 {
		t.Fatal("DateStyle observation skipped files or changed accounting", stored, reserved)
	}
	t.Logf("effective DateStyle=%s; exact stored timestamps/prefix/coverage preserved", effective)
}

func storageReadBatchSnapshot(t *testing.T, s *Store, c Config, p storageScanProgress) string {
	t.Helper()
	var snapshot string
	if err := s.DB.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'progress',(SELECT to_jsonb(p)||jsonb_build_object('xmin',p.xmin::text) FROM storage_scan_progress p WHERE data_dir=$1 AND scan_kind=$2 AND path=$3),
 'files',(SELECT COALESCE(jsonb_agg(to_jsonb(f)||jsonb_build_object('xmin',f.xmin::text) ORDER BY f.path),'[]'::jsonb) FROM storage_files f),
 'counters',(SELECT to_jsonb(c)||jsonb_build_object('xmin',c.xmin::text) FROM storage_counters c WHERE id=1),
 'reservations',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM storage_reservations r))::text`, c.DataDir, p.kind, p.path).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestStorageScanSessionReadBatchLookupFailureRollsBackAndRecovers(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	const files = storageRegistrationBatch*2 + 7
	storageSessionFiles(t, c, "sources", files)
	kinds := []string{"bootstrap_sources"}
	session := newStorageScanSession()
	t.Cleanup(session.close)
	nextSessionStep(t, s, c, session, kinds)
	handle := session.readers[kinds[0]].dir
	p, err := s.nextStorageScan(ctx, c, kinds, false)
	if err != nil {
		t.Fatal(err)
	}
	before := storageReadBatchSnapshot(t, s, c, p)
	var namespace string
	if err = s.DB.QueryRow(ctx, `SELECT current_schema()`).Scan(&namespace); err != nil {
		t.Fatal(err)
	}
	role := newID("scan_lookup_runtime")
	ident := pgx.Identifier{role}.Sanitize()
	if _, err = s.DB.Exec(ctx, `CREATE ROLE `+ident+` NOLOGIN`); err != nil {
		t.Fatal("owned permission fixture requires CREATEROLE", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.DB.Exec(cleanup, `DROP OWNED BY `+ident+`; DROP ROLE `+ident); err != nil {
			t.Error("owned lookup role cleanup", err)
		}
	})
	if _, err = s.DB.Exec(ctx, `GRANT USAGE ON SCHEMA `+pgx.Identifier{namespace}.Sanitize()+` TO `+ident+`; GRANT SELECT,UPDATE ON storage_scan_progress,storage_counters TO `+ident+`; GRANT SELECT ON sources,artifacts TO `+ident+`; GRANT INSERT,UPDATE ON storage_files TO `+ident); err != nil {
		t.Fatal(err)
	}
	config := s.DB.Config()
	config.MaxConns = 1
	previousConnect := config.AfterConnect
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if previousConnect != nil {
			if err := previousConnect(ctx, conn); err != nil {
				return err
			}
		}
		_, err := conn.Exec(ctx, `SET ROLE `+ident)
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	runtime := &Store{DB: db}
	var backend int32
	var effectiveRole string
	var canReadCursor, canUpdateCursor, canReadFiles, canLogin bool
	if err = db.QueryRow(ctx, `SELECT pg_backend_pid(),current_user,has_table_privilege(current_user,'storage_scan_progress','SELECT'),has_table_privilege(current_user,'storage_scan_progress','UPDATE'),has_table_privilege(current_user,'storage_files','SELECT'),(SELECT rolcanlogin FROM pg_roles WHERE rolname=current_user)`).Scan(&backend, &effectiveRole, &canReadCursor, &canUpdateCursor, &canReadFiles, &canLogin); err != nil {
		t.Fatal(err)
	}
	if effectiveRole != role || !canReadCursor || !canUpdateCursor || canReadFiles || canLogin {
		t.Fatal("owned role did not isolate the denied lookup", effectiveRole, canReadCursor, canUpdateCursor, canReadFiles, canLogin)
	}
	// The fixture locks only the cursor row, not the storage fence. Waiting on
	// that row proves the first batched cursor query executes before the denied
	// lookup. This is separate from the unchanged real5ms capacity probe.
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	if _, err = lock.Exec(ctx, `SELECT position FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND path=$3 FOR UPDATE`, c.DataDir, p.kind, p.path); err != nil {
		t.Fatal(err)
	}
	operation, cancel := context.WithTimeout(ctx, 3*time.Second)
	done := make(chan error, 1)
	finished := false
	go func() { done <- runtime.storageScanStepSession(operation, c, p, session) }()
	t.Cleanup(func() {
		cancel()
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("owned failed lookup operation was not joined")
			}
		}
	})
	observedLock := false
	witness, cancelWitness := context.WithTimeout(ctx, time.Second)
	defer cancelWitness()
	for witness.Err() == nil {
		select {
		case err = <-done:
			finished = true
			t.Fatal("lookup refused before the first cursor query reached its row lock", err)
		default:
		}
		if err = s.DB.QueryRow(witness, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND wait_event='transactionid' AND query LIKE 'SELECT position,prefix_digest,completed_at,started_at%')`, backend).Scan(&observedLock); err != nil {
			t.Fatal(err)
		}
		if observedLock {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancelWitness()
	if !observedLock {
		t.Fatal("first batched query did not reach the owned cursor row lock")
	}
	rollbackStorage(lock)
	select {
	case err = <-done:
		finished = true
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("owned failed lookup operation did not return after releasing the cursor lock")
	}
	cancel()
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "42501" || !strings.Contains(denied.Message, "storage_files") {
		t.Fatal("second lookup did not return the genuine table permission failure", err)
	}
	if after := storageReadBatchSnapshot(t, s, c, p); after != before {
		t.Fatal("failed lookup changed durable cursor, coverage or accounting")
	}
	assertStorageReaderClosed(t, handle)
	if len(session.readers) != 0 {
		t.Fatal("unacknowledged read batch retained a directory")
	}
	db.Close()
	if _, err = s.DB.Exec(ctx, `GRANT SELECT ON storage_files TO `+ident); err != nil {
		t.Fatal(err)
	}
	reopened, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	recovered := &Store{DB: reopened}
	counts := observeStorageReads(session)
	if err = recovered.storageScanStepSession(ctx, c, p, session); err != nil {
		t.Fatal("new call did not drain/rollback and recover the same cursor", err)
	}
	if got := storageProgressRow(t, s, c, p.kind, p.path); got.position != storageRegistrationBatch*2 || counts.replay[p.path] != storageRegistrationBatch {
		t.Fatal("permission recovery trusted a speculative OS position", got.position, counts.replay[p.path])
	}
	// The stale tuple still performs its bounded prefetch, but cannot publish
	// observations or advance any cursor/coverage/accounting row.
	stale := newStorageScanSession()
	t.Cleanup(stale.close)
	beforeStale := storageReadBatchSnapshot(t, s, c, p)
	if err = recovered.storageScanStepSession(ctx, c, p, stale); err != nil {
		t.Fatal("stale cursor with successful prefetch did not yield", err)
	}
	if storageReadBatchSnapshot(t, s, c, p) != beforeStale || len(stale.readers) != 0 {
		t.Fatal("stale prefetched cursor changed durable state or retained a reader")
	}
	current := storageProgressRow(t, s, c, p.kind, p.path)
	if err = recovered.storageScanStepSession(ctx, c, current, session); err != nil {
		t.Fatal("permission recovery did not finish its coverage", err)
	}
	if stored, reserved := ledgerBytes(t, s); stored != files || reserved != 0 || len(session.readers) != 0 {
		t.Fatal("permission recovery lost files, double charged or retained a handle", stored, reserved)
	}
}
