package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func benchmarkMetadata() (Source, platformInfo) {
	source, info := metadataCacheFixture()
	base := info.Formats
	info.Formats = make([]platformFormat, 182)
	for n := range info.Formats {
		f := base[n%len(base)]
		f.ID = fmt.Sprint(n)
		f.URL = "https://media.example/recording?token=" + strings.Repeat("a", 2000) + fmt.Sprint(n)
		info.Formats[n] = f
	}
	return source, info
}

// Compare actual pinned extractor startup/serialization with owner-scoped
// persisted reuse, using the same offline 182-format recording in both cases.
// This isolates repeated resolution overhead; it does not measure real sites.
func BenchmarkMetadataResolution(b *testing.B) {
	path := os.Getenv("CUTMY_TEST_YTDLP")
	if path == "" {
		b.Skip("set CUTMY_TEST_YTDLP to benchmark the pinned offline extractor")
	}
	path, err := exec.LookPath(path)
	if err != nil {
		b.Fatal("configured extractor is not executable")
	}
	ctx := context.Background()
	versionCtx, cancelVersion := context.WithTimeout(ctx, 10*time.Second)
	version, err := runCommand(versionCtx, path, "--version")
	cancelVersion()
	if err != nil || strings.TrimSpace(string(version)) != "2026.08.19" {
		b.Fatal("benchmark requires the pinned extractor")
	}
	s := metadataCacheBenchmarkStore(b)
	source, info := benchmarkMetadata()
	source.ID = newID("src")
	if err := s.AddSource(ctx, source); err != nil {
		b.Fatal(err)
	}
	fixture := metadataFixture()
	fixture["formats"] = info.Formats
	payload, err := json.Marshal(fixture)
	if err != nil {
		b.Fatal(err)
	}
	input := filepath.Join(b.TempDir(), "source.info.json")
	if err := os.WriteFile(input, payload, 0600); err != nil {
		b.Fatal(err)
	}
	g, err := newNetworkGuard(32 << 20)
	if err != nil {
		b.Fatal(err)
	}
	defer g.Close()
	args := metadataFixtureArgs(input, g.ProxyURL())
	resolve := func() (platformInfo, error) {
		resolveCtx, cancelResolve := context.WithTimeout(ctx, 30*time.Second)
		defer cancelResolve()
		payload, err := runCommand(resolveCtx, path, args...)
		if err != nil {
			return platformInfo{}, err
		}
		var got platformInfo
		if err := json.Unmarshal(payload, &got); err != nil {
			return got, err
		}
		return got, validatePlatformInfo(got)
	}
	info, err = resolve()
	if err != nil {
		b.Fatal(err)
	}
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		b.Fatal(err)
	}
	b.Run("uncached_extractor", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := resolve(); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(1, "resolutions/op")
	})
	b.Run("persisted_cache", func(b *testing.B) {
		calls := 0
		b.ReportAllocs()
		for range b.N {
			_, hit, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) { calls++; return resolve() })
			if err != nil || !hit {
				b.Fatal("persisted benchmark cache was not reused")
			}
		}
		b.ReportMetric(float64(calls)/float64(b.N), "resolutions/op")
	})
	if g.bytes.Load() != 0 {
		b.Fatal("offline benchmark made an upstream network request")
	}
}

func metadataCacheBenchmarkStore(b *testing.B) *Store {
	b.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		b.Skip("set TEST_DATABASE_URL for the persisted cache benchmark")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		b.Fatal(err)
	}
	schema := newID("bench")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		b.Fatal(err)
	}
	var s *Store
	b.Cleanup(func() {
		if s != nil {
			s.DB.Close()
		}
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(raw)
	if err != nil {
		b.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("pool_max_conns", "2")
	u.RawQuery = query.Encode()
	s, err = OpenStore(ctx, u.String())
	if err != nil {
		b.Fatal(err)
	}
	return s
}
