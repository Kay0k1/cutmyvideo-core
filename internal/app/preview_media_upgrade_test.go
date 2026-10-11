package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewMediaPolicyUpgradeDoesNotReuseLegacyCache(t *testing.T) {
	s := testStore(t)
	c, ctx := mcConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	cookie := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "cutmy_session", Value: cookie})
	owner, _ := ownerFromRequest(request)
	base := mcBaseVideo(t, ctx, c, "upgrade-input", 160, 96, "")
	input := filepath.Join(c.DataDir, "upgrade-rotated.mp4")
	mcRotateFixture(t, ctx, c, base, input, 90)
	source := Source{ID: newID("src"), Owner: owner, Kind: "upload", Path: input, DurationMS: 2000, Width: 160, Height: 96}
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	// Seed the exact key used before the media policy upgrade. Its bytes
	// represent a completed old rendition that must remain charged until
	// normal eviction, but must not be returned as a current-policy cache hit.
	digest := sha256.Sum256([]byte(source.ID + ":0"))
	legacyKey := hex.EncodeToString(digest[:])
	legacyID := newID("preview")
	if cached, err := s.acquirePreview(ctx, c, source, legacyID, legacyKey, false); err != nil || cached != nil {
		t.Fatalf("legacy cache setup was not admitted: %v", err)
	}
	legacy := cachedPreviewFixture(t, c, legacyID, 1024)
	legacyPath := legacy.Name()
	if err := s.publishPreview(ctx, c, source, legacyID, legacy); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	server := NewServer(c, s)
	handler := server.Handler()
	var first []byte
	for _, expected := range []string{"miss", "hit"} {
		if expected == "hit" {
			server.Config.FFmpeg = filepath.Join(c.DataDir, "absent-ffmpeg")
			server.Config.FFprobe = filepath.Join(c.DataDir, "absent-ffprobe")
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+source.ID+"/preview?start_ms=0", nil)
		r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: cookie})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != http.StatusOK || response.Header().Get("X-Preview-Cache") != expected {
			t.Fatalf("policy upgrade expected %s, got %d/%s: %s", expected, response.Code, response.Header().Get("X-Preview-Cache"), response.Body.String())
		}
		if expected == "miss" {
			first = append([]byte(nil), response.Body.Bytes()...)
			file := filepath.Join(c.DataDir, "current-policy-preview.mp4")
			if err := os.WriteFile(file, first, 0600); err != nil {
				t.Fatal(err)
			}
			mcSquareGeometry(t, ctx, c, file, 96, 160)
			frame, width, height := mcFrame(t, ctx, c, file, true)
			mcCheckCorners(t, frame, width, height, [4]int{1, 3, 0, 2})
		} else if string(first) != response.Body.String() {
			t.Fatal("current-policy cache did not reuse the new rendition")
		}
	}
	var charged int64
	if err := s.DB.QueryRow(context.Background(), "SELECT size_bytes FROM storage_reservations WHERE id=$1 AND kind='preview_cache'", legacyID).Scan(&charged); err != nil || charged != 1024 {
		t.Fatalf("old cache lost its retained accounting: %d %v", charged, err)
	}
	if info, err := os.Stat(legacyPath); err != nil || info.Size() != charged {
		t.Fatalf("old cache disappeared without normal eviction: %v", err)
	}
}
