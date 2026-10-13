package app

import (
	"context"
	"os"
	"sync"
	"time"
)

const localProbeCacheEntries = 32
const localProbeCacheTTL = 5 * time.Minute

type localProbeEntry struct {
	file    os.FileInfo
	info    probeInfo
	tool    string
	expires time.Time
}

// Only validated, bounded metadata is retained, never media or remote URLs.
// Source authorization and reader pins remain the responsibility of each caller.
type localProbeCache struct {
	mu      sync.Mutex
	entries map[string]localProbeEntry
}

func localProbeKey(source Source) string { return source.Owner + ":" + source.ID + ":" + source.Path }

func (s *Store) beginCachedLocal(ctx context.Context, c Config, source Source) (context.Context, *LocalExportSession, bool) {
	ctx, session := BeginLocalExport(ctx, source.Path)
	file, err := session.unchanged()
	if err != nil || ctx.Err() != nil {
		return ctx, session, false
	}
	s.localProbes.mu.Lock()
	defer s.localProbes.mu.Unlock()
	key := localProbeKey(source)
	entry, ok := s.localProbes.entries[key]
	if !ok {
		return ctx, session, false
	}
	if time.Now().After(entry.expires) || entry.tool != c.FFprobe || !os.SameFile(entry.file, file) || entry.file.Size() != file.Size() || !entry.file.ModTime().Equal(file.ModTime()) {
		delete(s.localProbes.entries, key)
		return ctx, session, false
	}
	session.file, session.info = file, entry.info
	return ctx, session, true
}

func (s *Store) rememberLocalProbe(ctx context.Context, c Config, source Source) {
	session := localExportSession(ctx, source.Path, false)
	if session == nil || ctx.Err() != nil {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.file == nil {
		return
	}
	if _, err := session.unchanged(); err != nil {
		return
	}
	s.localProbes.mu.Lock()
	defer s.localProbes.mu.Unlock()
	if s.localProbes.entries == nil {
		s.localProbes.entries = make(map[string]localProbeEntry)
	}
	now := time.Now()
	for key, entry := range s.localProbes.entries {
		if !now.Before(entry.expires) {
			delete(s.localProbes.entries, key)
		}
	}
	key := localProbeKey(source)
	if _, ok := s.localProbes.entries[key]; !ok && len(s.localProbes.entries) >= localProbeCacheEntries {
		var oldest string
		for candidate, entry := range s.localProbes.entries {
			if oldest == "" || entry.expires.Before(s.localProbes.entries[oldest].expires) {
				oldest = candidate
			}
		}
		delete(s.localProbes.entries, oldest)
	}
	s.localProbes.entries[key] = localProbeEntry{session.file, session.info, c.FFprobe, now.Add(localProbeCacheTTL)}
}
