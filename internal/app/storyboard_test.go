package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func storyboardFixture(t *testing.T, c Config) string {
	t.Helper()
	path := filepath.Join(c.DataDir, "storyboard-source.mp4")
	_, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-filter_threads", "1", "-f", "lavfi", "-i", "color=red:size=160x90:rate=25", "-vf", "drawbox=color=green:t=fill:enable='gte(t,5)',drawbox=color=blue:t=fill:enable='gte(t,10)',drawbox=color=yellow:t=fill:enable='gte(t,15)'", "-t", "32", "-c:v", "libx264", "-threads", "1", path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoryboardSamplesSourceTimelineAndPadsShortWindows(t *testing.T) {
	c := mediaConfig(t)
	input := storyboardFixture(t, c)
	for _, interval := range []Range{{StartMS: 0, EndMS: 30000}, {StartMS: 30000, EndMS: 32000}, {StartMS: 0, EndMS: 100}} {
		out := filepath.Join(c.DataDir, "storyboard.jpg")
		if err := renderStoryboardOptions(context.Background(), c, []mediaInput{{Path: input, OffsetMS: 8 * 3600000}}, Range{StartMS: 8*3600000 + interval.StartMS, EndMS: 8*3600000 + interval.EndMS}, out, ExportRequest{}); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		picture, format, err := image.Decode(file)
		file.Close()
		if err != nil || format != "jpeg" || picture.Bounds().Dx() != 960 || picture.Bounds().Dy() != 90 {
			t.Fatalf("invalid sprite: %s %v", format, err)
		}
		var wanted [][3]int
		switch interval.StartMS {
		case 30000:
			wanted = [][3]int{{255, 255, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}}
		default:
			wanted = [][3]int{{255, 0, 0}, {0, 128, 0}, {0, 0, 255}, {255, 255, 0}, {255, 255, 0}, {255, 255, 0}}
			if interval.EndMS == 100 {
				wanted = [][3]int{{255, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}}
			}
		}
		for index, color := range wanted {
			r, g, b, _ := picture.At(index*160+80, 45).RGBA()
			got := [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
			for channel := range got {
				if delta := got[channel] - color[channel]; delta < -12 || delta > 12 {
					t.Fatalf("interval %+v cell %d: RGB=%v want=%v", interval, index, got, color)
				}
			}
		}
	}
}

func TestStoryboardEndpointCachesAccountsAndProtectsOwnership(t *testing.T) {
	store := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	cookie := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	auth := httptest.NewRequest(http.MethodGet, "/", nil)
	auth.AddCookie(&http.Cookie{Name: "cutmy_session", Value: cookie})
	owner, _ := ownerFromRequest(auth)
	source := Source{ID: newID("src"), Owner: owner, Title: "Colors", Kind: "upload", DurationMS: 32000, Path: storyboardFixture(t, c)}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	app := NewServer(c, store)
	request := func(start, session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+source.ID+"/storyboard?start_ms="+start, nil)
		r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: session})
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	first := request("31000", cookie)
	if first.Code != 200 || first.Header().Get("Content-Type") != "image/jpeg" || first.Header().Get("X-Preview-Start-MS") != "30000" || first.Header().Get("X-Preview-End-MS") != "32000" || first.Header().Get("X-Storyboard-Step-MS") != "5000" || first.Header().Get("X-Storyboard-Frames") != "1" || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("storyboard contract: %d %v %s", first.Code, first.Header(), first.Body.String())
	}
	app.Config.FFmpeg, app.Config.FFprobe = "/unavailable", "/unavailable"
	second := request("30000", cookie)
	if second.Code != 200 || second.Header().Get("X-Preview-Cache") != "hit" || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("storyboard did not reuse cache: %d %s", second.Code, second.Body.String())
	}
	foreign := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if got := request("30000", foreign); got.Code != 404 {
		t.Fatalf("foreign storyboard exposed: %d", got.Code)
	}
	if got := request("32000", cookie); got.Code != 400 {
		t.Fatalf("invalid position accepted: %d", got.Code)
	}
	var active int
	var charged int64
	if err := store.DB.QueryRow(context.Background(), "SELECT count(*) FROM storage_reservations WHERE kind='preview'").Scan(&active); err != nil || active != 0 {
		t.Fatalf("active reservation leaked: %d %v", active, err)
	}
	if err := store.DB.QueryRow(context.Background(), "SELECT sum(size_bytes) FROM storage_reservations WHERE kind='preview_cache'").Scan(&charged); err != nil || charged != int64(first.Body.Len()) {
		t.Fatalf("sprite is not charged: %d %v", charged, err)
	}
	if _, err := store.DeleteSource(context.Background(), source.ID, owner); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(c.DataDir, "previews"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("source deletion retained sprites: %v %v", entries, err)
	}
}

func TestStoryboardYieldsBeforeInspectingBusySource(t *testing.T) {
	store := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	store.ConfigureStorage(c)
	source := storedSource(t, store, "owner")
	source.Width, source.Height = 160, 90
	source.Path = filepath.Join(c.DataDir, "do-not-probe.mp4")
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET path=$2,width=$3,height=$4 WHERE id=$1", source.ID, source.Path, source.Width, source.Height); err != nil {
		t.Fatal(err)
	}
	if _, err := store.acquirePreview(context.Background(), c, source, newID("preview"), "foreground", false); err != nil {
		t.Fatal(err)
	}
	c.FFprobe, c.FFmpeg = "/unavailable", "/unavailable"
	app := NewServer(c, store)
	r := httptest.NewRequest(http.MethodGet, "/storyboard?start_ms=0", nil)
	r.SetPathValue("id", source.ID)
	w := httptest.NewRecorder()
	app.sourceStoryboard(w, r, source.Owner)
	if w.Code != http.StatusTooManyRequests || !bytes.Contains(w.Body.Bytes(), []byte(`"server_busy"`)) {
		t.Fatalf("background inspection ran despite foreground work: %d %s", w.Code, w.Body.String())
	}
}

func TestUnknownInspectionKeepsFullPoolWhilePersistedDimensionsAreSmall(t *testing.T) {
	store := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	store.ConfigureStorage(c)
	source := storedSource(t, store, "owner")
	source.Width, source.Height = 160, 90
	source.Path = filepath.Join(c.DataDir, "historical-cover-source.mp4")
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET path=$2,width=$3,height=$4 WHERE id=$1", source.ID, source.Path, source.Width, source.Height); err != nil {
		t.Fatal(err)
	}
	unknown := source
	unknown.Width, unknown.Height = 0, 0
	if err := store.reserveSourceInspection(context.Background(), unknown, "full-inspection", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := store.acquirePreview(context.Background(), c, source, newID("preview"), "concurrent-render", false); !errors.Is(err, errSourceServerBusy) {
		t.Fatalf("full inspection admitted concurrent decoder: %v", err)
	}
	if err := store.reserveSourceInspection(context.Background(), source, "concurrent-inspection", time.Minute); !errors.Is(err, errSourceServerBusy) {
		t.Fatalf("full inspection admitted concurrent probe: %v", err)
	}
	if _, err := store.DB.Exec(context.Background(), "DELETE FROM storage_reservations WHERE id='full-inspection'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.acquirePreview(context.Background(), c, source, newID("preview"), "after-inspection", false); err != nil {
		t.Fatalf("released inspection retained pool capacity: %v", err)
	}
}
