package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHLSJobCacheReusesBytesButKeepsIntervalAndNetworkBudgets(t *testing.T) {
	guard, err := newNetworkGuard(64)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	var requests atomic.Int32
	guard.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("abcd")), ContentLength: 4, Header: http.Header{}}, nil
	})
	work := t.TempDir()
	ctx, closeCache, err := withHLSDownloadCache(context.Background(), work, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCache()
	fetch := func(name string, format platformFormat, downloaded *atomic.Int64, limit int64) error {
		return fetchHLSPayload(ctx, guard, format, "https://media.example/segment", filepath.Join(work, name), downloaded, limit)
	}
	var bytes atomic.Int64
	if err := fetch("first", platformFormat{}, &bytes, 16); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "first")); err != nil {
		t.Fatal(err)
	}
	if err := fetch("second", platformFormat{}, &bytes, 16); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || guard.bytes.Load() != 4 || bytes.Load() != 8 {
		t.Fatalf("requests=%d network=%d staged=%d", requests.Load(), guard.bytes.Load(), bytes.Load())
	}
	if err := fetch("overflow", platformFormat{}, &bytes, 10); err == nil {
		t.Fatal("cached bytes bypassed interval budget")
	}
	if guard.bytes.Load() != 4 {
		t.Fatal("cached read charged network transfer")
	}
	var other atomic.Int64
	if err := fetch("headers", platformFormat{Headers: map[string]string{"User-Agent": "other"}}, &other, 16); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("cache reused payload with different accepted headers")
	}
	t.Run("closed-payload-still-checks-modification-time", func(t *testing.T) {
		cache := ctx.Value(hlsDownloadCacheKey{}).(*hlsDownloadCache)
		key := hlsPayloadKey("https://media.example/segment", platformFormat{})
		entry := cache.entries[key]
		if entry == nil {
			t.Fatal("completed payload was not cached after close")
		}
		changed := entry.file.ModTime().Add(-time.Hour)
		if err := os.Chtimes(entry.path, changed, changed); err != nil {
			t.Fatal(err)
		}
		file, release := cache.open(key)
		if file != nil {
			release()
			t.Fatal("same-size cached payload bypassed modification-time validation")
		}
		if cache.bytes != 4 || len(cache.entries) != 1 {
			t.Fatal("invalid cache entry retained its charge", cache.bytes, len(cache.entries))
		}
		var retried atomic.Int64
		for _, name := range []string{"after-mutation", "after-mutation-cached"} {
			if err := fetch(name, platformFormat{}, &retried, 16); err != nil {
				t.Fatal(err)
			}
		}
		if requests.Load() != 3 || guard.bytes.Load() != 12 || retried.Load() != 8 {
			t.Fatal("changed payload was not replaced by one reusable closed download", requests.Load(), guard.bytes.Load(), retried.Load())
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		path := filepath.Join(work, "canceled-cache-hit")
		if err := fetchHLSPayload(canceled, guard, platformFormat{}, "https://media.example/segment", path, &retried, 16); !errors.Is(err, context.Canceled) {
			t.Fatal("canceled cache read did not preserve cancellation", err)
		}
		if requests.Load() != 3 || guard.bytes.Load() != 12 || retried.Load() != 8 {
			t.Fatal("canceled cache hit performed transfer or staging")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("canceled cache hit created a destination", err)
		}
	})
}

func TestHLSInitializationCacheHasImmutableMapAndBoundedPinnedEntries(t *testing.T) {
	guard, err := newNetworkGuard(64)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	var requests atomic.Int32
	guard.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("init")), ContentLength: 4, Header: http.Header{}}, nil
	})
	work := t.TempDir()
	ctx, closeCache, err := withHLSDownloadCache(context.Background(), work, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCache()
	for i, name := range []string{"first", "second"} {
		file, err := os.Create(filepath.Join(work, name))
		if err != nil {
			t.Fatal(err)
		}
		var downloaded atomic.Int64
		err = appendHLSInitialization(ctx, guard, platformFormat{}, "https://media.example/init", work, i, file, &downloaded, 16)
		if err == nil && i == 0 {
			_, err = file.WriteString("segment")
		}
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(work, "second"))
	if err != nil || string(data) != "init" || requests.Load() != 1 {
		t.Fatalf("map=%q requests=%d err=%v", data, requests.Load(), err)
	}
	cache := ctx.Value(hlsDownloadCacheKey{}).(*hlsDownloadCache)
	key := hlsPayloadKey("https://media.example/init", platformFormat{})
	file, release := cache.open(key)
	if file == nil {
		t.Fatal("map cache missing")
	}
	defer release()
	other := filepath.Join(work, "other")
	if err := os.WriteFile(other, []byte("more"), 0600); err != nil {
		t.Fatal(err)
	}
	cache.remember("other", other)
	if cache.bytes != 4 || len(cache.entries) != 1 {
		t.Fatal("pinned cache file was evicted or overcharged")
	}
	if _, ok := cache.entries[key]; !ok {
		t.Fatal("pinned entry removed")
	}
	t.Run("preexisting-map-is-not-owned-by-failed-download", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "map-0.part")
		if err := os.WriteFile(path, []byte("sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
		output, err := os.Create(filepath.Join(dir, "output"))
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		beforeRequests, beforeNetwork := requests.Load(), guard.bytes.Load()
		var downloaded atomic.Int64
		err = appendHLSInitialization(ctx, guard, platformFormat{}, "https://media.example/init", dir, 0, output, &downloaded, 16)
		if !errors.Is(err, os.ErrExist) {
			t.Fatal("preexisting map was not rejected by exclusive creation", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "sentinel" {
			t.Fatal("failed download removed or changed an unowned map", string(data), err)
		}
		if requests.Load() != beforeRequests || guard.bytes.Load() != beforeNetwork || downloaded.Load() != 0 {
			t.Fatal("preexisting map rejection performed a transfer")
		}
	})
}

func TestHLSJobCacheDisabledForSmallStores(t *testing.T) {
	for _, size := range []int64{0, 16 << 20, 256 << 20} {
		if hlsJobCacheBudget(Config{MaxStorageBytes: size}) != 0 {
			t.Fatal("small store cache changed admission")
		}
	}
	if got := hlsJobCacheBudget(Config{MaxStorageBytes: 1 << 30}); got != 32<<20 {
		t.Fatal("large store allowance", got)
	}
	work := t.TempDir()
	ctx, closeCache, err := withHLSDownloadCache(context.Background(), work, 0)
	defer closeCache()
	if err != nil || ctx.Value(hlsDownloadCacheKey{}) != nil {
		t.Fatal("disabled cache initialized")
	}
	files, _ := os.ReadDir(work)
	if len(files) != 0 {
		t.Fatal("disabled cache left workspace")
	}
}

func TestHLSPacketAnchorHonorsPreparationDecoderBudget(t *testing.T) {
	c := mediaConfig(t)
	c.WorkerConcurrency, c.WorkerPreparationConcurrency = 2, 1
	commands := countMediaCommands(t, &c)
	fixture := filepath.Join(c.DataDir, "anchor.mkv")
	setup, finishSetup := context.WithTimeout(context.Background(), 15*time.Second)
	defer finishSetup()
	if _, err := runCommand(setup, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color=red:size=160x90:rate=10", "-t", "0.2", "-c:v", "libx264", "-threads", "1", "-bf", "0", "-an", fixture); err != nil {
		t.Fatal(err)
	}
	info, _, err := probe(setup, c, fixture, false)
	if err != nil {
		t.Fatal(err)
	}
	before := len(commands())
	ctx := withWorkerMediaBudget(context.Background(), c)
	full := Source{Path: "large", Width: 7680, Height: 4320}
	release, err := acquireMediaProcessing(ctx, full, nil, ExportRequest{Format: "mp4"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release() }()
	format := platformFormat{Width: 160, Height: 90, VCodec: "h264"}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	_, err = hlsPreparedPacketAnchor(bounded, c, format, fixture, info)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("packet anchor bypassed the occupied decoder pool", err)
	}
	if got := len(commands()); got != before {
		t.Fatal("canceled preparation started a media command", before, got)
	}
	release()
	release = func() {}
	bounded, cancel = context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	anchor, err := hlsPreparedPacketAnchor(bounded, c, format, fixture, info)
	if err != nil || anchor != 0 {
		t.Fatal("released preparation could not obtain its packet anchor", anchor, err)
	}
	if got := len(commands()); got != before+1 {
		t.Fatal("successful preparation did not invoke exactly one probe", before, got)
	}
	// Both canceled and successful calls must return every acquired slot.
	all, err := acquireMediaProcessing(bounded, full, nil, ExportRequest{Format: "mp4"})
	if err != nil {
		t.Fatal("packet anchor leaked its decoder permit", err)
	}
	all()
}
