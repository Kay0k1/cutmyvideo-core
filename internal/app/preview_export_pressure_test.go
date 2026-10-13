package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifiedExportAdmissionDoesNotWaitForBusyPreviewPool(t *testing.T) {
	store := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	c.MutationsPerMinute = 100
	c.MaxActiveJobs = 4
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	identity := httptest.NewRequest("GET", "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	source := Source{ID: newID("src"), Owner: owner, Kind: "upload", Title: "Concurrent editing", DurationMS: 4000, Path: previewFixture(t, c)}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	commands := countMediaCommands(t, &c)
	server := NewServer(c, store)
	call := func(method, endpoint, body string) *httptest.ResponseRecorder {
		request := databaseRequest(method, endpoint, body, cookie)
		request.Header.Set("Idempotency-Key", "pressure-export")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	tracks := "/api/v1/sources/" + source.ID + "/tracks"
	if response := call("GET", tracks, ""); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var err error
	source, err = store.Source(context.Background(), source.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		id := newID("preview")
		if file, err := store.acquirePreview(context.Background(), c, source, id, fmt.Sprint(i), false); err != nil || file != nil {
			t.Fatalf("pressure admission: %v", err)
		}
		t.Cleanup(func() { _ = store.releasePreviewAdmission(context.Background(), c, source, id, true) })
	}
	if response := call("GET", tracks, ""); response.Code != 200 {
		t.Fatal("warm tracks blocked behind previews", response.Code, response.Body.String())
	}
	body := fmt.Sprintf(`{"source_id":%q,"ranges":[{"start_ms":1000,"end_ms":3000}],"format":"mp4","quality":"720p","cut_mode":"accurate","audio_track":1,"crop":{"x":0,"y":0,"width":1,"height":1},"cleanup_source":false}`, source.ID)
	response := call("POST", "/api/v1/jobs", body)
	if response.Code != 202 {
		t.Fatal("verified export blocked behind previews", response.Code, response.Body.String())
	}
	if replay := call("POST", "/api/v1/jobs", body); replay.Code != 202 || replay.Body.String() != response.Body.String() {
		t.Fatal("idempotent export was not retained")
	}
	for _, command := range commands()[1:] {
		if strings.HasPrefix(command, "ffprobe") {
			t.Fatal("track/export admission reran the source probe", commands())
		}
	}
	foreign := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))}
	denied := httptest.NewRecorder()
	server.Handler().ServeHTTP(denied, databaseRequest("GET", tracks, "", foreign))
	if denied.Code != 404 {
		t.Fatal("cached inspection bypassed source ownership", denied.Code)
	}
}
