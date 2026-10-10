package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type storageReadCounts struct {
	names, replay map[string]int
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
