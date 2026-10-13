package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// The server reserves this additional disk allowance once per platform job.
// Cache files belong to that job's workspace and disappear on cancellation or
// recovery cleanup. The cache never outlives the operation's guarded transport.
const hlsJobCacheBytes int64 = 32 << 20
const hlsJobCacheEntries = 128

func hlsJobCacheBudget(c Config) int64 {
	// Small installations retain their existing admission headroom. Larger
	// stores charge at most 1/32 of their total allowance, capped at 32 MiB.
	if c.MaxStorageBytes <= 256<<20 {
		return 0
	}
	return min(hlsJobCacheBytes, c.MaxStorageBytes/32)
}

type hlsDownloadCacheKey struct{}
type hlsCacheEntry struct {
	path    string
	file    os.FileInfo
	used    uint64
	readers int
}
type hlsDownloadCache struct {
	mu           sync.Mutex
	dir          string
	limit, bytes int64
	clock        uint64
	entries      map[string]*hlsCacheEntry
}

func withHLSDownloadCache(ctx context.Context, work string, limit int64) (context.Context, func(), error) {
	if limit <= 0 {
		return ctx, func() {}, nil
	}
	dir := filepath.Join(work, ".hls-cache")
	if err := os.Mkdir(dir, 0700); err != nil {
		return ctx, func() {}, err
	}
	cache := &hlsDownloadCache{dir: dir, limit: max(int64(0), limit), entries: make(map[string]*hlsCacheEntry)}
	return context.WithValue(ctx, hlsDownloadCacheKey{}, cache), func() { _ = os.RemoveAll(dir) }, nil
}

func hlsPayloadKey(raw string, format platformFormat) string {
	request := &http.Request{Header: make(http.Header)}
	mediaHeaders(request, format.Headers)
	// JSON gives deterministic ordering; only headers actually accepted by the
	// guarded fetch enter the key. Neither URLs nor keys are logged or exported.
	headers, _ := json.Marshal(request.Header)
	digest := sha256.Sum256(append(append([]byte(raw), 0), headers...))
	return hex.EncodeToString(digest[:])
}

func (c *hlsDownloadCache) open(key string) (*os.File, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return nil, func() {}
	}
	file, err := os.Open(entry.path)
	if err != nil {
		if os.IsNotExist(err) && entry.readers == 0 {
			delete(c.entries, key)
			c.bytes -= entry.file.Size()
		}
		return nil, func() {}
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, entry.file) || info.Size() != entry.file.Size() || !info.ModTime().Equal(entry.file.ModTime()) {
		_ = file.Close()
		if entry.readers == 0 {
			if err := os.Remove(entry.path); err == nil || os.IsNotExist(err) {
				delete(c.entries, key)
				c.bytes -= entry.file.Size()
			}
		}
		return nil, func() {}
	}
	c.clock++
	entry.used, entry.readers = c.clock, entry.readers+1
	return file, func() {
		_ = file.Close()
		c.mu.Lock()
		entry.readers--
		c.mu.Unlock()
	}
}

func (c *hlsDownloadCache) remember(key, path string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries[key] != nil {
		return
	}
	cachePath := filepath.Join(c.dir, key)
	// The completed stage is linked, not copied: no additional unbounded cache
	// producer runs alongside downloads. Link support is an optional optimization.
	if err := os.Link(path, cachePath); err != nil {
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(cachePath)
		}
	}()
	file, err := os.Open(cachePath)
	if err != nil {
		return
	}
	cachedInfo, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !cachedInfo.Mode().IsRegular() || cachedInfo.Size() != info.Size() || !cachedInfo.ModTime().Equal(info.ModTime()) {
		return
	}
	// Windows path-based Stat defers loading file IDs until SameFile, which
	// would reopen the producer path after staging has removed it. File.Stat
	// captures IDs eagerly and keeps the cached inode's identity independent.
	for len(c.entries) >= hlsJobCacheEntries || info.Size() > c.limit-c.bytes {
		var oldestKey string
		var oldest *hlsCacheEntry
		for candidate, entry := range c.entries {
			if entry.readers == 0 && (oldest == nil || entry.used < oldest.used) {
				oldestKey, oldest = candidate, entry
			}
		}
		if oldest == nil {
			return
		}
		if err := os.Remove(oldest.path); err != nil && !os.IsNotExist(err) {
			return
		}
		delete(c.entries, oldestKey)
		c.bytes -= oldest.file.Size()
	}
	c.clock++
	c.entries[key] = &hlsCacheEntry{path: cachePath, file: cachedInfo, used: c.clock}
	c.bytes += cachedInfo.Size()
	keep = true
}

// A payload becomes immutable only after its writer has closed successfully.
// Windows may finalize its last-write timestamp at close, and a cache identity
// captured sooner would immediately invalidate an otherwise reusable file.
func fetchHLSPayload(ctx context.Context, guard *networkGuard, format platformFormat, raw, path string, downloaded *atomic.Int64, limit int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	err = writeHLSPayload(ctx, guard, format, raw, output, downloaded, limit)
	closeErr := output.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if cache, _ := ctx.Value(hlsDownloadCacheKey{}).(*hlsDownloadCache); cache != nil {
		cache.remember(hlsPayloadKey(raw, format), path)
	}
	keep = true
	return nil
}

func writeHLSPayload(ctx context.Context, guard *networkGuard, format platformFormat, raw string, output *os.File, downloaded *atomic.Int64, limit int64) error {
	cache, _ := ctx.Value(hlsDownloadCacheKey{}).(*hlsDownloadCache)
	writer := &stagingWriter{writer: output, downloaded: downloaded, limit: limit}
	if cache == nil {
		return guard.fetch(ctx, raw, format.Headers, writer, limit)
	}
	key := hlsPayloadKey(raw, format)
	if file, release := cache.open(key); file != nil {
		defer release()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.Size() > limit {
			return errPlatformUnavailable
		}
		_, err = io.Copy(writer, contextReadSeeker{context: ctx, ReadSeeker: file})
		return err
	}
	return guard.fetch(ctx, raw, format.Headers, writer, limit)
}

func appendHLSInitialization(ctx context.Context, guard *networkGuard, format platformFormat, raw, dir string, stream int, output *os.File, downloaded *atomic.Int64, limit int64) error {
	path := filepath.Join(dir, fmt.Sprintf("map-%d.part", stream))
	if err := fetchHLSPayload(ctx, guard, format, raw, path, downloaded, limit); err != nil {
		return err
	}
	defer os.Remove(path)
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	// Initialization bytes have their own immutable inode. Linking the mutable
	// concatenation output would let later segments corrupt the cached map and
	// exceed its recorded allowance.
	_, err = io.Copy(output, contextReadSeeker{context: ctx, ReadSeeker: file})
	return err
}
