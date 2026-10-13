package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func metadataCacheFixture() (Source, platformInfo) {
	return Source{ID: "source", Owner: "owner", Kind: "platform", URL: "https://www.youtube.com/watch?v=fixture", ProviderID: "fixture", DurationMS: 1200000},
		platformInfo{ID: "fixture", Title: "Recording fixture", Duration: 1200, Extractor: "Youtube", Type: "video", LiveStatus: "not_live", Thumbnail: "https://media.example/thumbnail.jpg", Formats: []platformFormat{
			{ID: "303", URL: "https://media.example/video.webm?token=private-video", Protocol: "https", Ext: "webm", VCodec: "vp9", ACodec: "none", Height: 1080, Width: 1920, TBR: 2500, Headers: map[string]string{"User-Agent": "fixture-agent", "Cookie": "private-cookie"}},
			{ID: "251", URL: "https://media.example/audio.webm?token=private-audio", Protocol: "https", Ext: "webm", VCodec: "none", ACodec: "opus", ABR: 128, TBR: 128, Headers: map[string]string{"Referer": "https://media.example/recording"}},
		}}
}

func storedMetadataSource(t *testing.T, store *Store) (Source, platformInfo) {
	t.Helper()
	source, info := metadataCacheFixture()
	source.ID = newID("src")
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	return source, info
}

func TestMetadataResolutionInvalidationWaitsForPublisherAndRemovesFreshCache(t *testing.T) {
	for _, recent := range []bool{false, true} {
		t.Run(fmt.Sprintf("recent-%t", recent), func(t *testing.T) {
			s := testStore(t)
			source, info := storedMetadataSource(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if claimed, err := s.claimPlatformMetadataResolution(ctx, source, "publisher"); err != nil || !claimed {
				t.Fatal("fixture claim failed")
			}
			publisher, err := s.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackStorage(publisher)
			var token string
			if err := publisher.QueryRow(ctx, `SELECT token FROM source_metadata_resolutions WHERE source_id=$1 FOR UPDATE`, source.ID).Scan(&token); err != nil {
				t.Fatal(err)
			}
			// A separate one-connection resolver pool makes its blocked backend
			// identifiable without retaining any connection during upstream work.
			config := s.DB.Config()
			config.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal("independent invalidator pool failed")
			}
			defer pool.Close()
			var invalidatorPID int32
			if err := pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&invalidatorPID); err != nil {
				t.Fatal(err)
			}
			invalidator := &Store{DB: pool}
			done := make(chan error, 1)
			go func() {
				if recent {
					invalidator.invalidateRecentPlatformMetadata(ctx, source.Owner, source.URL)
					done <- nil
				} else {
					done <- invalidator.InvalidatePlatformMetadata(ctx, source)
				}
			}()
			blocked := false
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if err := s.DB.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, invalidatorPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !blocked {
				t.Fatal("invalidation did not wait on publishing lease")
			}
			if err := writePlatformMetadataCache(ctx, publisher, source, info, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := publisher.Exec(ctx, `DELETE FROM source_metadata_resolutions WHERE source_id=$1 AND token=$2`, source.ID, token); err != nil {
				t.Fatal(err)
			}
			if err := publisher.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("invalidation failed after publisher committed")
				}
			case <-ctx.Done():
				t.Fatal("invalidation failed to drain")
			}
			if _, hit := readPlatformMetadataCache(ctx, s.DB, source); hit {
				t.Fatal("invalidation retained cache inserted by the blocked publisher")
			}
			var claims int
			if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM source_metadata_resolutions WHERE source_id=$1`, source.ID).Scan(&claims); err != nil || claims != 0 {
				t.Fatal("invalidation retained resolution claim")
			}
		})
	}
}
func TestMetadataResolutionForceFreshInvalidationAndWinnerFence(t *testing.T) {
	for _, winner := range []bool{false, true} {
		t.Run(fmt.Sprintf("newer-winner-%t", winner), func(t *testing.T) {
			s := testStore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			source, info := storedMetadataSource(t, s)
			if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
				t.Fatal(err)
			}
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var first platformInfo
			var firstErr error
			go func() {
				defer close(done)
				first, firstErr = s.forceFreshPlatformMetadata(ctx, source, func() (platformInfo, error) {
					close(started)
					<-release
					return info, nil
				})
			}()
			defer func() {
				unblock()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("force-refresh resolver did not drain")
				}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("force-refresh upstream did not start")
			}
			var claims int
			if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM source_metadata_resolutions WHERE source_id=$1", source.ID).Scan(&claims); err != nil || claims != 1 {
				t.Fatal("force refresh did not persist a short claim before network work")
			}
			if winner {
				newer := info
				newer.Title = "Newer forced metadata"
				got, err := s.forceFreshPlatformMetadata(ctx, source, func() (platformInfo, error) { return newer, nil })
				if err != nil || got.Title != newer.Title {
					t.Fatal("newer force refresh failed")
				}
			} else if err := s.InvalidatePlatformMetadata(ctx, source); err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("older force refresh did not return fresh data")
			}
			if firstErr != nil || first.Title != info.Title || !first.cachedUntil.IsZero() {
				t.Fatal("cache fencing changed the force-refresh caller's fresh result")
			}
			cached, hit := readPlatformMetadataCache(ctx, s.DB, source)
			if winner {
				if !hit || cached.Title != "Newer forced metadata" {
					t.Fatal("older force refresh replaced the concurrent winner")
				}
			} else if hit {
				t.Fatal("invalidated force refresh republished its older cache")
			}
			if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM source_metadata_resolutions WHERE source_id=$1", source.ID).Scan(&claims); err != nil || claims != 0 {
				t.Fatal("completed force refresh retained a claim")
			}
		})
	}
}

func TestMetadataResolutionForceFreshUnavailableClaimReturnsFreshWithoutCaching(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		t.Fatal(err)
	}
	// A missing optional coordinator must not prevent a real fresh request or
	// let an unclaimed resolver overwrite the previously published cache.
	sourcePinExec(t, s.DB, "DROP TABLE source_metadata_resolutions")
	newer := info
	newer.Title = "Fresh without coordinator"
	calls := 0
	got, err := s.forceFreshPlatformMetadata(ctx, source, func() (platformInfo, error) {
		calls++
		return newer, nil
	})
	if err != nil || calls != 1 || got.Title != newer.Title || !got.cachedUntil.IsZero() {
		t.Fatal("unavailable optional claim prevented fresh resolution")
	}
	cached, hit := readPlatformMetadataCache(ctx, s.DB, source)
	if !hit || cached.Title != info.Title {
		t.Fatal("unclaimed force refresh changed persisted metadata")
	}
}

func TestMetadataCacheLifetimeAndStreamPreservation(t *testing.T) {
	source, info := metadataCacheFixture()
	now := time.Unix(1800000000, 0)
	got, expires, err := cacheablePlatformMetadata(source, info, now)
	if err != nil || !expires.Equal(now.Add(5*time.Minute)) || got.Thumbnail != info.Thumbnail || got.Entries != nil || len(got.Formats) != 2 {
		t.Fatal("bounded metadata did not remain cacheable")
	}
	selected, err := pickStreams(got, "1080p", "mp4")
	if err != nil || len(selected) != 2 || selected[0].ID != "303" || selected[1].ID != "251" || selected[0].Headers["Cookie"] != "private-cookie" {
		t.Fatal("cached metadata changed stream pairing or private request headers")
	}
	info.Formats[0].URL += fmt.Sprintf("&expire=%d", now.Add(2*time.Minute).Unix())
	_, expires, err = cacheablePlatformMetadata(source, info, now)
	if err != nil || !expires.Equal(now.Add(90*time.Second)) {
		t.Fatal("signed expiry did not bound the cache with a safety margin")
	}
	info.Formats[1].URL += fmt.Sprintf("&expire=%d", now.Add(10*time.Minute).Unix())
	_, expires, err = cacheablePlatformMetadata(source, info, now)
	if err != nil || !expires.Equal(now.Add(90*time.Second)) {
		t.Fatal("the earliest required signing deadline was not retained")
	}
}

func TestMetadataCacheSigningExpiry(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, tt := range []struct {
		name, query  string
		known, valid bool
		want         time.Time
	}{
		{"opaque", "token=private", false, true, time.Time{}},
		{"absolute", fmt.Sprintf("expire=%d", now.Unix()), true, true, now},
		{"multiple", fmt.Sprintf("expires=%d&exp=%d", now.Add(time.Minute).Unix(), now.Unix()), true, true, now},
		{"duplicate", fmt.Sprintf("expire=%d&expire=%d", now.Add(time.Minute).Unix(), now.Unix()), true, true, now},
		{"malformed", "expire=unknown", true, false, time.Time{}},
		{"negative", "expire=-1", true, false, time.Time{}},
		{"aws", "X-Amz-Date=" + now.Format("20060102T150405Z") + "&X-Amz-Expires=60", true, true, now.Add(time.Minute)},
		{"gcs", "X-Goog-Date=" + now.Format("20060102T150405Z") + "&X-Goog-Expires=120", true, true, now.Add(2 * time.Minute)},
		{"missing-date", "X-Amz-Expires=60", true, false, time.Time{}},
		{"unbounded", "X-Goog-Date=" + now.Format("20060102T150405Z") + "&X-Goog-Expires=99999999999", true, false, time.Time{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse("https://media.example/file?" + tt.query)
			if err != nil {
				t.Fatal(err)
			}
			got, known, valid := signedMetadataExpiry(u)
			if known != tt.known || valid != tt.valid || !got.Equal(tt.want) {
				t.Fatal("signing lifetime was parsed incorrectly")
			}
		})
	}
}

func TestMetadataCacheFreshResolutionRejectsChangedIdentity(t *testing.T) {
	source, info := metadataCacheFixture()
	for _, tt := range []struct {
		name   string
		change func(*platformInfo)
		code   string
	}{
		{"id", func(i *platformInfo) { i.ID = "changed" }, "source_changed"},
		{"duration", func(i *platformInfo) { i.Duration += 2 }, "source_changed"},
		{"live", func(i *platformInfo) { i.IsLive = true }, "live_not_supported"},
		{"collection", func(i *platformInfo) { i.Entries = json.RawMessage(`[]`) }, "unsupported_collection"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad := info
			tt.change(&bad)
			if _, err := freshPlatformMetadata(source, func() (platformInfo, error) { return bad, nil }); exportProblem(err).code != tt.code {
				t.Fatal("fresh metadata changed the original source contract")
			}
		})
	}
	info.cachedUntil = time.Now().Add(time.Minute)
	fresh, err := freshPlatformMetadata(source, func() (platformInfo, error) { return info, nil })
	if err != nil || !fresh.cachedUntil.IsZero() {
		t.Fatal("fresh metadata retained the prior cache deadline")
	}
}

func TestMetadataCacheHLSImportRefreshesDeniedAddressOnce(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:12\n#EXTINF:12,\nsegment.ts\n#EXT-X-ENDLIST\n"
	for _, tt := range []struct {
		name                              string
		cached                            bool
		oldStatus, newStatus, wantRefresh int
		limited                           bool
	}{
		{"cached-denial", true, 403, 200, 1, false},
		{"fresh-denial", false, 403, 200, 0, false},
		{"second-denial", true, 403, 403, 1, false},
		{"upstream-unavailable", true, 503, 200, 0, false},
		{"shared-transfer-budget", true, 403, 200, 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g, err := newNetworkGuard(1 << 20)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			if tt.limited {
				g.limit = int64(len(manifest))
			}
			g.bytes.Store(7)
			oldCalls, newCalls, refreshes := 0, 0, 0
			g.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
				status := tt.oldStatus
				if r.URL.Path == "/new.m3u8" {
					status = tt.newStatus
					newCalls++
				} else {
					oldCalls++
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, ContentLength: int64(len(manifest)), Body: io.NopCloser(strings.NewReader(manifest))}, nil
			})
			source, info := metadataCacheFixture()
			source.DurationMS, info.Duration = 12000, 12
			info.Formats = []platformFormat{{ID: "hls", URL: "https://media.example/old.m3u8?token=private", Protocol: "m3u8_native", Ext: "mp4", VCodec: "h264", ACodec: "aac", Height: 1080}}
			got, duration, err := inspectCachedPlatformSource(context.Background(), g, info, tt.cached, func() (platformInfo, error) {
				refreshes++
				fresh := info
				fresh.Formats = append([]platformFormat(nil), info.Formats...)
				fresh.Formats[0].URL = "https://media.example/new.m3u8?token=private-new"
				return freshPlatformMetadata(source, func() (platformInfo, error) { return fresh, nil })
			})
			if refreshes != tt.wantRefresh || oldCalls != 1 || newCalls != tt.wantRefresh {
				t.Fatal("HLS inspection retried outside its cache-only one-shot boundary")
			}
			if tt.name == "cached-denial" {
				if err != nil || duration != 12000 || !strings.Contains(got.Formats[0].URL, "/new.m3u8") || g.bytes.Load() != 7+int64(len(manifest)) {
					t.Fatal("fresh HLS address or original transfer budget was lost")
				}
			} else if err == nil {
				t.Fatal("terminal upstream or transfer failure was ignored")
			}
			if tt.limited && g.bytes.Load() <= g.limit {
				t.Fatal("refresh reset the original transfer budget")
			}
		})
	}
}

func TestMetadataCacheRejectsChangedAndUnusableSources(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Source, *platformInfo)
	}{
		{"identity", func(_ *Source, i *platformInfo) { i.ID = "different" }},
		{"duration", func(_ *Source, i *platformInfo) { i.Duration += 2 }},
		{"live", func(_ *Source, i *platformInfo) { i.IsLive = true }},
		{"upcoming", func(_ *Source, i *platformInfo) { i.LiveStatus = "is_upcoming" }},
		{"drm", func(_ *Source, i *platformInfo) { i.HasDRM = true }},
		{"collection", func(_ *Source, i *platformInfo) { i.Entries = json.RawMessage(`[]`) }},
		{"nan-duration", func(_ *Source, i *platformInfo) { i.Duration = math.NaN() }},
		{"upload", func(s *Source, _ *platformInfo) { s.Path = "/data/file.mp4" }},
		{"no-owner", func(s *Source, _ *platformInfo) { s.Owner = "" }},
		{"unusable-protocol", func(_ *Source, i *platformInfo) {
			for n := range i.Formats {
				i.Formats[n].Protocol = "http_dash_segments"
			}
		}},
		{"relay-address", func(_ *Source, i *platformInfo) {
			for n := range i.Formats {
				i.Formats[n].URL = "http://127.0.0.1:1234/relay/private"
			}
		}},
		{"private-address", func(_ *Source, i *platformInfo) {
			for n := range i.Formats {
				i.Formats[n].URL = "https://127.0.0.1/file"
			}
		}},
		{"expired", func(_ *Source, i *platformInfo) {
			for n := range i.Formats {
				i.Formats[n].URL += "&expire=1"
			}
		}},
		{"expiry-margin", func(_ *Source, i *platformInfo) {
			for n := range i.Formats {
				i.Formats[n].URL += fmt.Sprintf("&expire=%d", time.Now().Add(10*time.Second).Unix())
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source, info := metadataCacheFixture()
			tt.change(&source, &info)
			if _, _, err := cacheablePlatformMetadata(source, info, time.Now()); !errors.Is(err, errMetadataNotCacheable) {
				t.Fatal("unsafe or changed source remained cacheable")
			}
		})
	}
	// Unsupported entries can coexist with a complete, supported pair; their
	// process-local addresses must never survive persistence.
	source, info := metadataCacheFixture()
	info.Formats = append(info.Formats, platformFormat{ID: "relay", URL: "http://127.0.0.1:1234/relay/private", Protocol: "https", Ext: "mp4"})
	got, _, err := cacheablePlatformMetadata(source, info, time.Now())
	if err != nil || len(got.Formats) != 2 {
		t.Fatal("unusable relay was retained")
	}
}

func TestMetadataCachePersistsPrivatelyAndScopesOwners(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		t.Fatal(err)
	}
	// A new connection pool models a separate worker or restart: no in-memory
	// state is required to reuse the import result.
	restarted, err := OpenStore(ctx, s.DB.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.DB.Close()
	got, hit, err := restarted.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) {
		t.Error("persisted metadata was re-resolved")
		return platformInfo{}, errors.New("unexpected")
	})
	if err != nil || !hit || got.Formats[0].URL != info.Formats[0].URL || got.Formats[0].Headers["Cookie"] != "private-cookie" {
		t.Fatal("restart lost private metadata")
	}
	public, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-video", "private-audio", "private-cookie", "media.example"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("private stream data escaped through source JSON")
		}
	}
	other := source
	other.Owner = "another-owner"
	if _, hit := readPlatformMetadataCache(ctx, s.DB, other); hit {
		t.Fatal("another owner read the cache")
	}
	if _, _, err := restarted.resolvePlatformMetadata(ctx, other, func() (platformInfo, error) { t.Error("unauthorized resolution ran"); return info, nil }); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing ownership was not rejected")
	}
	if err := s.InvalidatePlatformMetadata(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); !hit {
		t.Fatal("another owner invalidated the cache")
	}
	if err := s.CachePlatformMetadata(ctx, other, info); err != nil {
		t.Fatal(err)
	}
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); !hit {
		t.Fatal("another owner replaced the cache")
	}
	if err := s.InvalidatePlatformMetadata(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); hit {
		t.Fatal("owner invalidation failed")
	}
}

func TestMetadataCacheRepeatedImportsKeepOriginalDeadline(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE source_metadata_cache SET expires_at=now()+interval '90 seconds' WHERE source_id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	var original time.Time
	if err := s.DB.QueryRow(ctx, `SELECT expires_at FROM source_metadata_cache WHERE source_id=$1`, source.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got, hit := s.RecentPlatformMetadata(ctx, source.Owner, source.URL)
		if !hit || got.ID != info.ID || got.Thumbnail != info.Thumbnail || !got.cachedUntil.Equal(original) {
			t.Fatal("reopening the same link lost its metadata or original deadline")
		}
		encoded, err := json.Marshal(got)
		if err != nil || strings.Contains(string(encoded), "cachedUntil") {
			t.Fatal("private in-memory cache deadline was serialized")
		}
		copy := source
		copy.ID = newID("src")
		if err := s.AddSource(ctx, copy); err != nil {
			t.Fatal(err)
		}
		if err := s.CachePlatformMetadata(ctx, copy, got); err != nil {
			t.Fatal(err)
		}
		var copied time.Time
		if err := s.DB.QueryRow(ctx, `SELECT expires_at FROM source_metadata_cache WHERE source_id=$1`, copy.ID).Scan(&copied); err != nil || !copied.Equal(original) {
			t.Fatal("reopening a link extended its cache lifetime")
		}
	}
	for _, lookup := range []struct{ owner, raw string }{
		{"other-owner", source.URL},
		{source.Owner, "https://www.youtube.com/watch?v=different"},
		{source.Owner, "http://127.0.0.1/relay/private"},
	} {
		if _, hit := s.RecentPlatformMetadata(ctx, lookup.owner, lookup.raw); hit {
			t.Fatal("recent import crossed the owner or exact URL boundary")
		}
	}
	if _, err := s.DB.Exec(ctx, `UPDATE source_metadata_cache SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, hit := s.RecentPlatformMetadata(ctx, source.Owner, source.URL); hit {
		t.Fatal("expired repeated imports were reused")
	}
	info.cachedUntil = time.Now().Add(-time.Second)
	if err := s.CachePlatformMetadata(ctx, source, info); !errors.Is(err, errMetadataNotCacheable) {
		t.Fatal("expired metadata was renewed on copy")
	}
}

func TestMetadataCacheConcurrentMissesResolveOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	other, err := OpenStore(ctx, s.DB.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer other.DB.Close()
	workers := []*Store{s, other}
	var calls atomic.Int32
	start := make(chan struct{})
	type result struct {
		info platformInfo
		hit  bool
		err  error
	}
	results := make(chan result, 8)
	var group sync.WaitGroup
	for n := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			got, hit, err := workers[n%len(workers)].resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { calls.Add(1); time.Sleep(20 * time.Millisecond); return info, nil })
			results <- result{got, hit, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	misses := 0
	for r := range results {
		if r.err != nil || r.info.ID != info.ID {
			t.Fatal("concurrent source resolution failed")
		}
		if !r.hit {
			misses++
		}
		// Every hit decodes its own map, so one request cannot modify a later one.
		r.info.Formats[0].Headers["User-Agent"] = "request-local"
	}
	if calls.Load() != 1 || misses != 1 {
		t.Fatal("concurrent cache misses repeated upstream resolution")
	}
	got, hit := readPlatformMetadataCache(ctx, s.DB, source)
	if !hit || got.Formats[0].Headers["User-Agent"] != "fixture-agent" {
		t.Fatal("request mutation changed persisted metadata")
	}
}

func TestMetadataCacheLockWaitFallsBackWithinBound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, _, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) {
			close(started)
			<-release
			return info, nil
		})
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		close(release)
		t.Fatal("initial resolver did not start")
	}
	begin := time.Now()
	_, hit, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { return info, nil })
	close(release)
	if err != nil || hit || time.Since(begin) > 4*time.Second {
		t.Fatal("cache lock delayed or failed independent fresh resolution")
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestMetadataResolutionDoesNotRetainPoolConnections(t *testing.T) {
	s := testStore(t) // The fixture has only two pool connections.
	ctx := context.Background()
	first, info := storedMetadataSource(t, s)
	second := first
	second.ID = newID("src")
	if err := s.AddSource(ctx, second); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	finished := make(chan error, 2)
	for _, source := range []Source{first, second} {
		go func() {
			_, _, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) {
				started <- struct{}{}
				<-release
				return info, nil
			})
			finished <- err
		}()
	}
	defer func() {
		close(release)
		for range 2 {
			if err := <-finished; err != nil {
				t.Error("resolution failed after releasing the upstream", err)
			}
		}
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(4 * time.Second):
			t.Fatal("two independent upstream resolutions did not start")
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	var result int
	if err := s.DB.QueryRow(queryCtx, `SELECT 1`).Scan(&result); err != nil || result != 1 {
		t.Fatal("upstream work exhausted the database pool", err)
	}
}

func TestMetadataResolutionExpiredAndInvalidatedOwnersCannotPublish(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	if claimed, err := s.claimPlatformMetadataResolution(ctx, source, "first"); err != nil || !claimed {
		t.Fatal("initial resolver claim failed", claimed, err)
	}
	sourcePinExec(t, s.DB, `UPDATE source_metadata_resolutions SET expires_at=clock_timestamp()-interval '1 second' WHERE source_id=$1`, source.ID)
	if claimed, err := s.claimPlatformMetadataResolution(ctx, source, "second"); err != nil || !claimed {
		t.Fatal("expired resolver was not reclaimed", claimed, err)
	}
	_ = s.cacheResolvedPlatformMetadata(ctx, source, "first", info)
	s.releasePlatformMetadataResolution(source, "first")
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); hit {
		t.Fatal("old resolution published after replacement")
	}
	var token string
	if err := s.DB.QueryRow(ctx, `SELECT token FROM source_metadata_resolutions WHERE source_id=$1`, source.ID).Scan(&token); err != nil || token != "second" {
		t.Fatal("old cleanup removed the replacement claim", token, err)
	}
	if err := s.InvalidatePlatformMetadata(ctx, source); err != nil {
		t.Fatal(err)
	}
	_ = s.cacheResolvedPlatformMetadata(ctx, source, "second", info)
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); hit {
		t.Fatal("forced refresh permitted an old resolver publication")
	}
	if claimed, err := s.claimPlatformMetadataResolution(ctx, source, "third"); err != nil || !claimed {
		t.Fatal("invalidation retained a claim", claimed, err)
	}
	if err := s.cacheResolvedPlatformMetadata(ctx, source, "third", info); err != nil {
		t.Fatal(err)
	}
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); !hit {
		t.Fatal("current resolver could not publish")
	}
	var pending int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM source_metadata_resolutions`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("successful resolution retained its claim", pending, err)
	}
}

func TestMetadataCacheExpiryCorruptionAndFailureFallback(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	for _, tt := range []struct {
		name   string
		change func()
	}{
		{"ttl", func() {
			if _, err := s.DB.Exec(ctx, `UPDATE source_metadata_cache SET expires_at=now()-interval '1 second'`); err != nil {
				t.Fatal(err)
			}
		}},
		{"json", func() {
			if _, err := s.DB.Exec(ctx, `UPDATE source_metadata_cache SET payload=$1`, []byte("invalid")); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed-id", func() { bad := info; bad.ID = "changed"; replaceCachedMetadata(t, s, source, bad) }},
		{"changed-duration", func() { bad := info; bad.Duration += 2; replaceCachedMetadata(t, s, source, bad) }},
		{"live", func() { bad := info; bad.IsLive = true; replaceCachedMetadata(t, s, source, bad) }},
		{"expired-url", func() {
			_, bad := metadataCacheFixture()
			bad.Formats[0].URL += "&expire=1"
			replaceCachedMetadata(t, s, source, bad)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
				t.Fatal(err)
			}
			tt.change()
			calls := 0
			_, hit, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { calls++; return info, nil })
			if err != nil || hit || calls != 1 {
				t.Fatal("stale or corrupt cache was not freshly resolved")
			}
		})
	}
	if err := s.InvalidatePlatformMetadata(ctx, source); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("controlled upstream failure")
	if _, hit, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { return platformInfo{}, failure }); hit || !errors.Is(err, failure) {
		t.Fatal("upstream failure changed")
	}
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); hit {
		t.Fatal("failed resolution was cached")
	}
	c := ledgerConfig(t)
	path := metadataRetentionArtifact(t, s, c)
	if _, err := s.DB.Exec(ctx, `DROP TABLE source_metadata_cache`); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { return info, nil }); err != nil || hit {
		t.Fatal("unavailable cache failed a valid fresh resolution")
	}
	paths, err := s.Cleanup(ctx, time.Hour, time.Hour)
	var databaseErr *pgconn.PgError
	if !errors.As(err, &databaseErr) || databaseErr.Code != "42P01" || len(paths) != 1 || paths[0] != path {
		t.Fatal("unavailable cache hid its cause or stopped committed media retention", paths, err)
	}
	var artifact, pending bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts WHERE id='cache-error-artifact'),
 EXISTS(SELECT 1 FROM storage_files WHERE path=$1 AND delete_pending)`, path).Scan(&artifact, &pending); err != nil || artifact || !pending {
		t.Fatal("unavailable cache prevented the media retention transaction", artifact, pending, err)
	}
	if err := s.DrainStorageDeletes(ctx, c, paths); err != nil {
		t.Fatal("unavailable cache prevented draining committed media paths", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("media survived cache-independent retention", err)
	}
}

func replaceCachedMetadata(t *testing.T, s *Store, source Source, info platformInfo) {
	t.Helper()
	payload, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(context.Background(), `UPDATE source_metadata_cache SET payload=$1 WHERE source_id=$2`, payload, source.ID); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataCacheBoundedPayloadAndSourceDeletion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	info.Formats[0].Headers["X-Large"] = strings.Repeat("x", platformMetadataCacheLimit)
	if err := s.CachePlatformMetadata(ctx, source, info); !errors.Is(err, errMetadataNotCacheable) {
		t.Fatal("oversized metadata was not bounded")
	}
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); hit {
		t.Fatal("oversized metadata was stored")
	}
	_, info = metadataCacheFixture()
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		t.Fatal(err)
	}
	// Expired rows are purged while their still-active source remains reusable.
	if _, err := s.DB.Exec(ctx, `UPDATE source_metadata_cache SET expires_at=now()-interval '1 second' WHERE source_id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cleanup(ctx, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, hit := readPlatformMetadataCache(ctx, s.DB, source); hit {
		t.Fatal("expired cache row survived maintenance")
	}
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		t.Fatal(err)
	}
	var lifetime float64
	if err := s.DB.QueryRow(ctx, `SELECT extract(epoch FROM expires_at-now()) FROM source_metadata_cache WHERE source_id=$1`, source.ID).Scan(&lifetime); err != nil || lifetime <= 0 || lifetime > 300 {
		t.Fatal("stored cache lifetime exceeded five minutes")
	}
	if _, err := s.DB.Exec(ctx, `UPDATE sources SET created_at=now()-interval '2 hours' WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cleanup(ctx, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM source_metadata_cache WHERE source_id=$1`, source.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("source deletion retained signed cache URLs")
	}
	if _, _, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { t.Error("deleted source resolved"); return info, nil }); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted source cache was authorized")
	}
}

func TestMetadataCacheRejectsChangedSourceBeforeResolution(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := storedMetadataSource(t, s)
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE sources SET provider_id='replacement' WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { t.Error("changed source resolved"); return info, nil }); exportProblem(err).code != "source_changed" {
		t.Fatal("changed source identity was not rejected")
	}
}
