package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

var metadataPayloadBenchmarkInfo platformInfo

func metadataPayloadFixture(unique bool) (Source, platformInfo) {
	source, info := benchmarkMetadata()
	if !unique {
		return source, info
	}
	for n := range info.Formats {
		var token strings.Builder
		for block := 0; token.Len() < 2000; block++ {
			hash := sha256.Sum256([]byte(fmt.Sprintf("format:%d:block:%d", n, block)))
			token.WriteString(base64.RawURLEncoding.EncodeToString(hash[:]))
		}
		info.Formats[n].URL = "https://media.example/recording?token=" + token.String()[:2000]
	}
	return source, info
}

func BenchmarkMetadataPayload(b *testing.B) {
	benchmarkMetadataPayloadDecode(b, false)
}

func BenchmarkMetadataPayloadUniqueTokens(b *testing.B) {
	benchmarkMetadataPayloadDecode(b, true)
}

func benchmarkMetadataPayloadDecode(b *testing.B, unique bool) {
	_, info := metadataPayloadFixture(unique)
	legacy, err := json.Marshal(info)
	if err != nil {
		b.Fatal(err)
	}
	compressed, err := encodePlatformMetadata(info)
	if err != nil {
		b.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		data []byte
	}{{"legacy_JSON", legacy}, {"optimized", compressed}} {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				var ok bool
				metadataPayloadBenchmarkInfo, ok = decodePlatformMetadata(tt.data)
				if !ok {
					b.Fatal("decode failed")
				}
			}
			b.ReportMetric(float64(len(tt.data)), "stored-B/op")
		})
	}
}

// Measure existing PostgreSQL TOAST compression separately from wire bytes.
// Both paths retain authorization, rendition validation and the same deadline.
func BenchmarkMetadataPayloadPersisted(b *testing.B) {
	benchmarkMetadataPayloadDatabase(b, false)
}

func BenchmarkMetadataPayloadPersistedUniqueTokens(b *testing.B) {
	benchmarkMetadataPayloadDatabase(b, true)
}

func benchmarkMetadataPayloadDatabase(b *testing.B, unique bool) {
	s := metadataCacheBenchmarkStore(b)
	ctx := context.Background()
	source, info := metadataPayloadFixture(unique)
	source.ID = newID("src")
	if err := s.AddSource(ctx, source); err != nil {
		b.Fatal(err)
	}
	legacy, err := json.Marshal(info)
	if err != nil {
		b.Fatal(err)
	}
	compressed, err := encodePlatformMetadata(info)
	if err != nil {
		b.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		data []byte
	}{{"legacy_JSON", legacy}, {"optimized", compressed}} {
		b.Run(tt.name, func(b *testing.B) {
			expires := time.Now().Add(platformMetadataCacheTTL)
			if _, err := s.DB.Exec(ctx, `INSERT INTO source_metadata_cache(source_id,owner,payload,expires_at) VALUES($1,$2,$3,$4)
 ON CONFLICT(source_id) DO UPDATE SET payload=EXCLUDED.payload,expires_at=EXCLUDED.expires_at`, source.ID, source.Owner, tt.data, expires); err != nil {
				b.Fatal(err)
			}
			var diskBytes int
			if err := s.DB.QueryRow(ctx, "SELECT pg_column_size(payload) FROM source_metadata_cache WHERE source_id=$1", source.ID).Scan(&diskBytes); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, hit, err := s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) {
					return platformInfo{}, fmt.Errorf("unexpected cache miss")
				})
				if err != nil || !hit || len(got.Formats) != len(info.Formats) {
					b.Fatal("persisted metadata did not resolve from cache")
				}
			}
			b.ReportMetric(float64(diskBytes), "physical-B/op")
			b.ReportMetric(float64(len(tt.data)), "wire-B/op")
		})
	}
}

func BenchmarkRecentPlatformMetadata(b *testing.B) {
	s := metadataCacheBenchmarkStore(b)
	ctx := context.Background()
	source, info := metadataCacheFixture()
	source.ID = newID("src")
	if err := s.AddSource(ctx, source); err != nil {
		b.Fatal(err)
	}
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		b.Fatal(err)
	}
	legacy := func() (platformInfo, bool) {
		if _, err := validateURL(source.URL); err != nil {
			return platformInfo{}, false
		}
		lookupCtx, cancel := context.WithTimeout(ctx, platformMetadataDBTimeout)
		defer cancel()
		prior := Source{Owner: source.Owner, URL: source.URL}
		err := s.DB.QueryRow(lookupCtx, `SELECT source.id,source.provider_id,source.duration_ms FROM sources source
 JOIN source_metadata_cache cache ON cache.source_id=source.id
 WHERE source.owner=$1 AND cache.owner=$1 AND source.url=$2 AND source.path='' AND cache.expires_at>now()
 ORDER BY cache.expires_at DESC LIMIT 1`, source.Owner, source.URL).Scan(&prior.ID, &prior.ProviderID, &prior.DurationMS)
		if err != nil {
			return platformInfo{}, false
		}
		return readPlatformMetadataCache(lookupCtx, s.DB, prior)
	}
	for _, tt := range []struct {
		name    string
		resolve func() (platformInfo, bool)
		queries float64
	}{{"legacy_two_queries", legacy, 2}, {"one_query", func() (platformInfo, bool) { return s.RecentPlatformMetadata(ctx, source.Owner, source.URL) }, 1}} {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, hit := tt.resolve(); !hit {
					b.Fatal("metadata lookup missed its cache")
				}
			}
			b.ReportMetric(tt.queries, "queries/op")
		})
	}
}
