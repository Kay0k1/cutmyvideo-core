package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

func TestPreviewEndpointOwnsBoundsAndCleansItsWorkspace(t *testing.T) {
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
	server := NewServer(c, s).Handler()
	for _, tc := range []struct {
		start, owner string
		code         int
	}{
		{"1250", cookie, 200}, {"4000", cookie, 400}, {"-1", cookie, 400}, {"9223372036854775807", cookie, 400}, {"0", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), 404},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+source.ID+"/preview?start_ms="+tc.start, nil)
		request.AddCookie(&http.Cookie{Name: "cutmy_session", Value: tc.owner})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != tc.code {
			t.Fatalf("preview %s: got %d: %s", tc.start, response.Code, response.Body.String())
		}
		if response.Code == 200 && (response.Header().Get("Content-Type") != "video/mp4" || response.Header().Get("X-Preview-Start-MS") != "1250" || response.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("preview lost its interval or privacy headers")
		}
	}
	var reservations int
	if err := s.DB.QueryRow(context.Background(), "SELECT count(*) FROM storage_reservations WHERE kind='preview'").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("preview leaked reservations: %d %v", reservations, err)
	}
	entries, err := os.ReadDir(filepath.Join(c.DataDir, "previews"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("preview leaked files: %v %v", entries, err)
	}
}

func TestPreviewReservationsProtectSourcesAndRecoverAbandonedFiles(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "owner")
	id := newID("preview")
	if err := s.reservePreview(ctx, c, source, id); err != nil {
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
	if _, err := s.DB.Exec(ctx, "UPDATE storage_reservations SET expires_at=now()-interval '1 second' WHERE id=$1", id); err != nil {
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
