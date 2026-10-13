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
		file, err := os.Create(filepath.Join(work, name))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		return fetchHLSPayload(ctx, guard, format, "https://media.example/segment", file, downloaded, limit)
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
