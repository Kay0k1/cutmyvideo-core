package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewGeometryIgnoresMalformedMetadataAndEnforcesSize(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("not a geometry box"), append(make([]byte, 4), []byte("freeCMYG\x00\x00\x00\x02\x00\x00\x00\x02")...)} {
		if w, h := readPreviewGeometry(bytes.NewReader(raw), int64(len(raw))); w != 0 || h != 0 {
			t.Fatalf("accepted malformed metadata %dx%d", w, h)
		}
	}
	var box [previewGeometryBytes]byte
	binary.BigEndian.PutUint32(box[:4], previewGeometryBytes)
	copy(box[4:12], "freeCMYG")
	for _, dims := range [][2]uint32{{1, 100}, {100, 0}, {0xffffffff, 0xffffffff}} {
		binary.BigEndian.PutUint32(box[12:16], dims[0])
		binary.BigEndian.PutUint32(box[16:20], dims[1])
		if w, h := readPreviewGeometry(bytes.NewReader(box[:]), int64(len(box))); w != 0 || h != 0 {
			t.Fatal("accepted unsafe geometry", dims)
		}
	}
	path := filepath.Join(t.TempDir(), "at-limit.mp4")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(previewOutputBytes - previewGeometryBytes); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err = appendPreviewGeometry(path, 160, 90); err != errOutputLimit {
		t.Fatal("metadata crossed total output limit", err)
	}
}

func TestPreviewScalingKeepsExactHeightCapDespiteFloatRounding(t *testing.T) {
	stream := &probeStream{Width: 242, Height: 804, SampleAspectRatio: "1:1"}
	width, height, err := videoOutputGeometry(stream, "best", true)
	if err != nil || width != 144 || height != 480 {
		t.Fatalf("preview cap lost pixels to floating point: %dx%d %v", width, height, err)
	}
}

func TestPreviewOriginalDisplayGeometrySurvivesChargedToolFreeCache(t *testing.T) {
	store := testStore(t)
	c, ctx := mcConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	// Coded 402x242, SAR 2:1, rotated 90: original display 242x804.
	// The browser rendition is only 144x480 (its aspect ratio is rounded);
	// its dimensions cannot
	// provide the original geometry required by normalized crop presets.
	base := mcBaseVideo(t, ctx, c, "preview-display-base", 402, 242, "setsar=2")
	input := filepath.Join(c.DataDir, "preview-display-rotated.mp4")
	mcRotateFixture(t, ctx, c, base, input, 90)
	cookie := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	auth := httptest.NewRequest("GET", "/", nil)
	auth.AddCookie(&http.Cookie{Name: "cutmy_session", Value: cookie})
	owner, _ := ownerFromRequest(auth)
	source := Source{ID: newID("src"), Owner: owner, Kind: "upload", Path: input, DurationMS: 2000, Width: 402, Height: 242}
	if err := store.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	server := NewServer(c, store)
	for _, expected := range []string{"miss", "hit"} {
		if expected == "hit" {
			server.Config.FFmpeg, server.Config.FFprobe = "/no-media-tools", "/no-media-tools"
		}
		r := httptest.NewRequest("GET", "/api/v1/sources/"+source.ID+"/preview?start_ms=0", nil)
		r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: cookie})
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, r)
		if response.Code != 200 || response.Header().Get("X-Preview-Cache") != expected || response.Header().Get("X-Source-Display-Width") != "242" || response.Header().Get("X-Source-Display-Height") != "804" {
			t.Fatalf("%s original geometry lost: %d %+v %s", expected, response.Code, response.Header(), response.Body.String())
		}
		var charged int64
		if err := store.DB.QueryRow(context.Background(), "SELECT sum(size_bytes) FROM storage_reservations WHERE kind='preview_cache'").Scan(&charged); err != nil || charged != int64(response.Body.Len()) {
			t.Fatalf("metadata uncharged: %d bytes vs %d %v", charged, response.Body.Len(), err)
		}
		out := filepath.Join(c.DataDir, "decoded-"+expected+".mp4")
		if err := os.WriteFile(out, response.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		mcSquareGeometry(t, ctx, c, out, 144, 480)
		frame, w, h := mcFrame(t, ctx, c, out, true)
		mcCheckCorners(t, frame, w, h, [4]int{1, 3, 0, 2})
		mcRun(t, ctx, c, "-i", out, "-f", "null", "-")
	}
}
