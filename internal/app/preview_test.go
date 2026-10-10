package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func previewFixture(t *testing.T, c Config) string {
	t.Helper()
	path := filepath.Join(c.DataDir, "input.mkv")
	_, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=960x540:rate=25", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "4", "-c:v", "mpeg4", "-threads", "1", "-c:a", "pcm_s16le", path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPreviewTranscodesUnsupportedBrowserCodecsAndOffsets(t *testing.T) {
	c := mediaConfig(t)
	input := previewFixture(t, c)
	out := filepath.Join(c.DataDir, "preview.mp4")
	// This staged input starts at hour eight on the original timeline.
	r := Range{StartMS: 8*3600000 + 1250, EndMS: 8*3600000 + 3500}
	if err := renderPreview(context.Background(), c, []mediaInput{{Path: input, OffsetMS: 8 * 3600000}}, r, out); err != nil {
		t.Fatal(err)
	}
	p, duration, err := probe(context.Background(), c, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if duration < 2150 || duration > 2350 {
		t.Fatalf("unexpected preview duration: %d", duration)
	}
	var video, audio bool
	for _, stream := range p.Streams {
		if stream.CodecType == "video" {
			video = stream.CodecName == "h264" && stream.Height <= 480 && stream.Width <= 854
		}
		if stream.CodecType == "audio" {
			audio = stream.CodecName == "aac"
		}
	}
	if !video || !audio {
		t.Fatalf("preview is not browser-compatible: %+v", p.Streams)
	}
}

func TestPreviewEndpointCachesCanonicalWindowsAndPreservesOwnership(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	cookie := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	auth := httptest.NewRequest(http.MethodGet, "/", nil)
	auth.AddCookie(&http.Cookie{Name: "cutmy_session", Value: cookie})
	owner, _ := ownerFromRequest(auth)
	source := Source{ID: newID("src"), Owner: owner, Title: "Recording", Kind: "upload", DurationMS: 4000, Path: previewFixture(t, c)}
	if err := s.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	app := NewServer(c, s)
	server := app.Handler()
	for _, tc := range []struct {
		start, owner string
		code         int
	}{
		{"1250", cookie, 200}, {"2500", cookie, 200}, {"4000", cookie, 400}, {"-1", cookie, 400}, {"9223372036854775807", cookie, 400}, {"0", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), 404},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+source.ID+"/preview?start_ms="+tc.start, nil)
		request.AddCookie(&http.Cookie{Name: "cutmy_session", Value: tc.owner})
		response := httptest.NewRecorder()
		started := time.Now()
		server.ServeHTTP(response, request)
		if response.Code != tc.code {
			t.Fatalf("preview %s: got %d: %s", tc.start, response.Code, response.Body.String())
		}
		if response.Code == 200 && (response.Header().Get("Content-Type") != "video/mp4" || response.Header().Get("X-Preview-Start-MS") != "0" || response.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("preview lost its interval or privacy headers")
		}
		if response.Code == 200 {
			cache := "miss"
			if tc.start == "2500" {
				cache = "hit"
			}
			if response.Header().Get("X-Preview-Cache") != cache {
				t.Fatalf("expected preview cache %s", cache)
			}
			// The second seek must work without running a media process.
			app.Config.FFmpeg = "/does-not-exist"
			server = NewServer(app.Config, s).Handler()
			t.Logf("preview cache %s: %s, %d bytes", cache, time.Since(started), response.Body.Len())
		}
	}
	var reservations int
	if err := s.DB.QueryRow(context.Background(), "SELECT count(*) FROM storage_reservations WHERE kind='preview'").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("preview leaked reservations: %d %v", reservations, err)
	}
	entries, err := os.ReadDir(filepath.Join(c.DataDir, "previews"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("preview cache missing: %v %v", entries, err)
	}
	var charge int64
	if err := s.DB.QueryRow(context.Background(), "SELECT size_bytes FROM storage_reservations WHERE kind='preview_cache'").Scan(&charge); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(c.DataDir, "previews", entries[0].Name(), "preview.mp4"))
	if err != nil || info.Size() != charge || charge >= previewOutputBytes {
		t.Fatalf("cache accounting does not match the retained MP4: %d %v", charge, err)
	}
	if _, err := s.DeleteSource(context.Background(), source.ID, owner); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(filepath.Join(c.DataDir, "previews"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("source deletion retained private preview files: %v %v", entries, err)
	}
}

func TestPreviewReservationsProtectSourcesAndRecoverAbandonedFiles(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	source.Width, source.Height = 1920, 1080
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET width=$2,height=$3 WHERE id=$1", source.ID, source.Width, source.Height); err != nil {
		t.Fatal(err)
	}
	id := newID("preview")
	if err := s.reservePreview(ctx, c, source, id); err != nil {
		t.Fatal(err)
	}
	second := newID("preview")
	if err := s.reservePreview(ctx, c, source, second); err != nil {
		t.Fatal(err)
	}
	if err := s.reservePreview(ctx, c, source, newID("preview")); !errors.Is(err, errSourceServerBusy) {
		t.Fatalf("unbounded preview processing: %v", err)
	}
	if _, err := s.DeleteSource(ctx, source.ID, source.Owner); !errors.Is(err, ErrSourceInUse) {
		t.Fatalf("active preview lost its source: %v", err)
	}
	dir := filepath.Join(c.DataDir, "previews", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abandoned.mp4"), []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupPreviews(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("maintenance removed an active preview")
	}
	if _, err := s.DB.Exec(ctx, "UPDATE storage_reservations SET expires_at=now()-interval '1 second' WHERE id=ANY($1)", []string{id, second}); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupPreviews(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("abandoned workspace remains")
	}
	if _, err := s.DeleteSource(ctx, source.ID, source.Owner); err != nil {
		t.Fatal(err)
	}
}

func TestLargeUploadLimitDoesNotInflateRemoteExportReservations(t *testing.T) {
	c := Config{MaxSourceBytes: 32 << 30, MaxFetchBytes: 1 << 30, MaxOutputBytes: 1 << 30}
	reserve, err := remainingJobReserve(c, Job{Items: make([]JobItem, 2)}, true)
	if err != nil || reserve != 4<<30 {
		t.Fatalf("selected interval reserved the whole recording: %d %v", reserve, err)
	}
}

func TestKnownUploadSizeUsesActualReservation(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxSourceBytes, c.MaxOwnerBytes = 32<<30, 64<<30
	ctx := context.Background()
	if err := s.reserveSource(ctx, c, "owner", "upload", false, 1024); err != nil {
		t.Fatal(err)
	}
	_, reserved := ledgerBytes(t, s)
	if reserved != 1024 {
		t.Fatalf("small upload reserved %d bytes", reserved)
	}
}

func TestPreviewCacheDeduplicatesAndYieldsCapacityToForeground(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	source.Width, source.Height = 1920, 1080
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET width=$2,height=$3 WHERE id=$1", source.ID, source.Width, source.Height); err != nil {
		t.Fatal(err)
	}
	id, key := newID("preview"), previewCacheKey(source, 0)
	if file, err := s.acquirePreview(ctx, c, source, id, key, false); err != nil || file != nil {
		t.Fatalf("first request was not admitted: %v", err)
	}
	if _, err := s.acquirePreview(ctx, c, source, newID("preview"), key, false); !errors.Is(err, errPreviewPending) {
		t.Fatalf("duplicate preview was not coalesced: %v", err)
	}
	if _, err := s.acquirePreview(ctx, c, source, newID("preview"), "background", true); !errors.Is(err, errSourceServerBusy) {
		t.Fatalf("background consumed foreground capacity: %v", err)
	}
	if _, err := s.acquirePreview(ctx, c, source, newID("preview"), "foreground", false); err != nil {
		t.Fatalf("foreground was blocked by one active preview: %v", err)
	}
	file := cachedPreviewFixture(t, c, id, 1024)
	defer file.Close()
	if err := s.publishPreview(ctx, c, source, id, file); err != nil {
		t.Fatal(err)
	}
	cached, err := s.acquirePreview(ctx, c, source, newID("preview"), key, true)
	if err != nil || cached == nil {
		t.Fatalf("completed duplicate did not reuse its cache: %v", err)
	}
	defer cached.Close()
	// Eviction must not interrupt an authorized response holding an open file.
	if _, err := s.DB.Exec(ctx, "UPDATE storage_reservations SET expires_at=now()-interval '1 second' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupPreviews(ctx, c); err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(cached); err != nil || len(data) != 1024 {
		t.Fatalf("eviction interrupted an authorized response: %d %v", len(data), err)
	}
	if _, err := os.Stat(file.Name()); !os.IsNotExist(err) {
		t.Fatal("expired cache retained disk space")
	}
}

func cachedPreviewFixture(t *testing.T, c Config, id string, size int64) *os.File {
	t.Helper()
	dir, err := previewDirectory(c, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "preview.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		file.Close()
		t.Fatal(err)
	}
	return file
}

func TestPreviewCacheBoundsOwnerBytesAndEntries(t *testing.T) {
	for _, size := range []int64{1024, 15 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := testStore(t)
			c := ledgerConfig(t)
			c.MaxStorageBytes = 1 << 30
			s.ConfigureStorage(c)
			ctx := context.Background()
			source := storedSource(t, s, "owner")
			source.Width, source.Height = 1920, 1080
			if _, err := s.DB.Exec(ctx, "UPDATE sources SET width=$2,height=$3 WHERE id=$1", source.ID, source.Width, source.Height); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < previewOwnerCacheEntries+3; i++ {
				id := newID("preview")
				if err := s.reservePreview(ctx, c, source, id); err != nil {
					t.Fatal(err)
				}
				file := cachedPreviewFixture(t, c, id, size)
				err := s.publishPreview(ctx, c, source, id, file)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			var count int
			var bytes int64
			if err := s.DB.QueryRow(ctx, "SELECT count(*),COALESCE(sum(size_bytes),0) FROM storage_reservations WHERE kind='preview_cache'").Scan(&count, &bytes); err != nil {
				t.Fatal(err)
			}
			if count > previewOwnerCacheEntries || bytes > previewOwnerCacheBytes {
				t.Fatalf("cache grew past bounds: %d files, %d bytes", count, bytes)
			}
			entries, err := os.ReadDir(filepath.Join(c.DataDir, "previews"))
			if err != nil || len(entries) != count {
				t.Fatalf("eviction left files outside accounting: %d %d %v", len(entries), count, err)
			}
			_, reserved := ledgerBytes(t, s)
			if reserved != bytes {
				t.Fatalf("cache retained processing reservations: %d vs %d", reserved, bytes)
			}
		})
	}
}

func TestPreviewLargeUploadedDecodersUseTheWholePool(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	source.Width, source.Height = 7680, 4320
	if _, err := s.DB.Exec(ctx, "UPDATE sources SET width=$2,height=$3 WHERE id=$1", source.ID, source.Width, source.Height); err != nil {
		t.Fatal(err)
	}
	if err := s.reservePreview(ctx, c, source, newID("preview")); err != nil {
		t.Fatal(err)
	}
	if err := s.reservePreview(ctx, c, source, newID("preview")); !errors.Is(err, errSourceServerBusy) {
		t.Fatalf("large decoders overlapped: %v", err)
	}
}
