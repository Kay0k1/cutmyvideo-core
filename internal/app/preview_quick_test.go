package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQuickPreviewUsesIndependentAlignedCacheAndWaitsForTrackInspection(t *testing.T) {
	store := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	input := filepath.Join(c.DataDir, "quick.mkv")
	if _, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "12", "-c:v", "mpeg4", "-threads", "1", "-c:a", "pcm_s16le", input); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	auth := httptest.NewRequest("GET", "/", nil)
	auth.AddCookie(cookie)
	owner, _ := ownerFromRequest(auth)
	source := Source{ID: newID("src"), Owner: owner, Title: "Quick", Kind: "upload", DurationMS: 12000, Path: input}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	server := NewServer(c, store)
	inspection := source
	inspection.Width, inspection.Height = 0, 0
	release, err := server.pinSourceInspection(context.Background(), inspection, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	timer := time.AfterFunc(150*time.Millisecond, release)
	defer timer.Stop()
	get := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+source.ID+"/preview?start_ms=7500"+query, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	quick := get("&window_ms=6000")
	if quick.Code != 200 || quick.Header().Get("X-Preview-Start-MS") != "6000" || quick.Header().Get("X-Preview-End-MS") != "12000" || quick.Header().Get("X-Preview-Cache") != "miss" {
		t.Fatalf("quick window/inspection wait: %d %s", quick.Code, quick.Body.String())
	}
	full := get("")
	if full.Code != 200 || full.Header().Get("X-Preview-Start-MS") != "0" || full.Header().Get("X-Preview-Cache") != "miss" {
		t.Fatalf("ordinary window reused quick cache: %d", full.Code)
	}
	server.Config.FFmpeg, server.Config.FFprobe = "/absent", "/absent"
	for _, query := range []string{"", "&window_ms=6000"} {
		cached := get(query)
		if cached.Code != 200 || cached.Header().Get("X-Preview-Cache") != "hit" {
			t.Fatalf("window cache failed without media tools: %d %s", cached.Code, cached.Body.String())
		}
	}
	for _, query := range []string{"&window_ms=0", "&window_ms=6000&window_ms=30000", "&window_ms=6001"} {
		if invalid := get(query); invalid.Code != 400 {
			t.Fatalf("invalid window accepted: %s %d", query, invalid.Code)
		}
	}
	quickPath := filepath.Join(c.DataDir, "quick.mp4")
	if err := os.WriteFile(quickPath, quick.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	_, duration, err := probe(context.Background(), c, quickPath, false)
	if err != nil || duration < 5900 || duration > 6100 || bytes.Equal(quick.Body.Bytes(), full.Body.Bytes()) {
		t.Fatalf("quick bytes/duration: %d %v", duration, err)
	}
}
