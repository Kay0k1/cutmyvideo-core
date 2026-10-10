package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

const storageScanReaders = 3

type storageScanRead struct {
	path   string
	names  int
	replay bool
}

// A session is local to one sequential public operation, never shared through
// Store. One reader per class bounds handles even when work discovery produces
// arbitrarily many descendant directories. Changing paths evicts that class.
type storageScanSession struct {
	readers map[string]*storageScanReader
	observe func(storageScanRead)
	commit  func(context.Context, pgx.Tx) error
}

type storageScanReader struct {
	dir       *os.File
	identity  os.FileInfo
	expected  storageScanProgress
	prefix    storagePrefix
	committed bool
}

func newStorageScanSession() *storageScanSession {
	return &storageScanSession{
		readers: make(map[string]*storageScanReader, storageScanReaders),
		commit:  func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) },
	}
}

func (s *storageScanSession) discard(kind string) {
	if reader := s.readers[kind]; reader != nil {
		reader.dir.Close()
		delete(s.readers, kind)
	}
}

func (s *storageScanSession) close() {
	for kind := range s.readers {
		s.discard(kind)
	}
}

func sameStorageGeneration(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func sameStorageCursor(a, b storageScanProgress) bool {
	return a.kind == b.kind && a.path == b.path && a.position == b.position && a.digest == b.digest &&
		a.started.Equal(b.started) && sameStorageGeneration(a.mtime, b.mtime) && sameStorageGeneration(a.size, b.size)
}

func (s *storageScanSession) accept(p storageScanProgress) bool {
	reader := s.readers[p.kind]
	if reader == nil || reader.expected.path != p.path || reader.prefix.count != p.position || reader.prefix.encoded() != p.digest {
		return false
	}
	reader.expected, reader.committed = p, true
	return true
}

func (s *storageScanSession) readNames(reader *storageScanReader, count int, replay bool) ([]os.DirEntry, error) {
	entries, err := reader.dir.ReadDir(count)
	if s.observe != nil {
		s.observe(storageScanRead{path: reader.expected.path, names: len(entries), replay: replay})
	}
	for _, entry := range entries {
		reader.prefix.add(entry)
	}
	return entries, err
}

// Persisted ordinals are not native directory cookies. A new session verifies
// the prefix in fixed chunks once; subsequent acknowledged batches advance the
// same live reader. Restarts, path switches and external cursor changes retain
// the O(prefix) verification cost instead of trusting a portable seek offset.
func (s *storageScanSession) read(ctx context.Context, c Config, p storageScanProgress) ([]os.DirEntry, string, os.FileInfo, bool, bool, error) {
	if err := ctx.Err(); err != nil {
		s.discard(p.kind)
		return nil, "", nil, false, false, err
	}
	root := scanRoot(c, p.kind)
	if p.kind == "cleanup_work" {
		root = filepath.Join(c.DataDir, "work")
	}
	if err := safeScanPath(root, p.path); err != nil {
		s.discard(p.kind)
		return nil, "", nil, false, false, err
	}
	info, err := os.Lstat(p.path)
	if os.IsNotExist(err) {
		s.discard(p.kind)
		return nil, p.digest, nil, true, false, nil
	}
	if err != nil {
		s.discard(p.kind)
		return nil, "", nil, false, false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		s.discard(p.kind)
		return nil, "", nil, false, false, errors.New("storage scan root is not a directory")
	}
	reset := p.mtime == nil || p.size == nil || *p.mtime != info.ModTime().UnixNano() || *p.size != info.Size()
	reader := s.readers[p.kind]
	if reader != nil && (reset || !reader.committed || !sameStorageCursor(reader.expected, p) || !os.SameFile(reader.identity, info)) {
		s.discard(p.kind)
		reader = nil
	}
	if reader == nil {
		if reset {
			p.position, p.digest = 0, ""
		}
		// Only the known groups (at most three classes) use sessions, but enforce
		// the resource limit independently if another caller supplies a new kind.
		if len(s.readers) >= storageScanReaders {
			s.close()
		}
		dir, e := os.Open(p.path)
		if e != nil {
			return nil, "", nil, false, false, e
		}
		opened, e := dir.Stat()
		if e != nil || !os.SameFile(info, opened) {
			dir.Close()
			return nil, "", nil, false, false, errors.New("storage directory changed while opening")
		}
		reader = &storageScanReader{dir: dir, identity: info, expected: p}
		s.readers[p.kind] = reader
		for remaining := p.position; remaining > 0; {
			if err = ctx.Err(); err != nil {
				s.discard(p.kind)
				return nil, "", nil, false, false, err
			}
			entries, e := s.readNames(reader, int(min(int64(storageScanBatchSize), remaining)), true)
			remaining -= int64(len(entries))
			if e != nil || len(entries) == 0 {
				s.discard(p.kind)
				if e != nil && !errors.Is(e, io.EOF) {
					return nil, "", nil, false, false, e
				}
				return nil, "", info, false, true, nil
			}
		}
		if reader.prefix.encoded() != p.digest {
			s.discard(p.kind)
			return nil, "", info, false, true, nil
		}
	}
	// Reading may move the OS buffer past this batch. Until COMMIT succeeds,
	// even a caller holding the old SQL cursor must reopen and verify its prefix.
	reader.committed = false
	batchLimit := storageRegistrationBatch
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < 100*time.Millisecond {
		batchLimit = storageScanBatchSize
	}
	entries := make([]os.DirEntry, 0, batchLimit)
	for len(entries) < batchLimit {
		if err = ctx.Err(); err != nil {
			s.discard(p.kind)
			return nil, "", nil, false, false, err
		}
		batch, e := s.readNames(reader, storageScanBatchSize, false)
		if e != nil && !errors.Is(e, io.EOF) {
			s.discard(p.kind)
			return nil, "", nil, false, false, e
		}
		entries = append(entries, batch...)
		if errors.Is(e, io.EOF) {
			digest := reader.prefix.encoded()
			s.discard(p.kind)
			return entries, digest, info, true, reset, nil
		}
	}
	return entries, reader.prefix.encoded(), info, false, reset, nil
}
