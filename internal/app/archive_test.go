package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestArchivePreservesOrderAcrossPartialRetriesAndOwnerAfterMediaRemoval(t *testing.T) {
	store := testStore(t)
	config := Config{DataDir: t.TempDir(), ArtifactTTL: time.Hour, MaxRanges: 128, MaxOutputBytes: 16 << 20}
	server := NewServer(config, store)
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	identity := httptest.NewRequest("GET", "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	source := storedSource(t, store, owner)
	create := func(source Source, contents []string) (Job, []Artifact) {
		t.Helper()
		request := requestFor(source)
		request.Ranges = make([]Range, len(contents)+1)
		for i := range request.Ranges {
			request.Ranges[i] = Range{Label: "same", StartMS: int64(i * 1000), EndMS: int64((i + 1) * 1000)}
		}
		job, err := store.CreateJob(context.Background(), owner, request, "")
		if err != nil {
			t.Fatal(err)
		}
		job, token, err := store.Claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		artifacts := make([]Artifact, len(contents))
		for i, content := range contents {
			file := filepath.Join(t.TempDir(), "clip.mp4")
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			a := Artifact{ID: newID("artifact"), Filename: "../worker-generated.mp4", SizeBytes: int64(len(content)), ActualStartMS: int64(i * 1000), ActualEndMS: int64((i + 1) * 1000)}
			if err := store.AddArtifact(context.Background(), owner, job.ID, file, token, a); err != nil {
				t.Fatal(err)
			}
			artifacts[i] = a
			job.Items[i].Status, job.Items[i].Artifact = "succeeded", &artifacts[i]
		}
		job.Items[len(contents)].Status = "failed"
		job.Status, job.Stage = "failed", "finished"
		if err := store.SaveJob(context.Background(), job, token); err != nil {
			t.Fatal(err)
		}
		return job, artifacts
	}
	first, previous := create(source, []string{"first", "second"})
	retry, current := create(source, []string{"third"})
	other, unrelated := create(storedSource(t, store, owner), []string{"unrelated"})
	_ = other
	if _, err := store.DB.Exec(context.Background(), `UPDATE sources SET media_removed=true WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	request := func(url string, differentOwner bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		if differentOwner {
			r.AddCookie(&http.Cookie{Name: cookie.Name, Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))})
		} else {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, contents []string) {
		t.Helper()
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("archive response: %d %s", w.Code, w.Body.String())
		}
		reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil || len(reader.File) != len(contents) {
			t.Fatalf("archive entries: %v", err)
		}
		for i, file := range reader.File {
			if file.Name != archiveEntryName(i, "same.mp4") || file.Method != zip.Store {
				t.Fatalf("unsafe/duplicate/compressed entry: %+v", file.FileHeader)
			}
			entry, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(entry)
			_ = entry.Close()
			if err != nil || string(data) != contents[i] {
				t.Fatalf("archive content %d: %s %v", i, data, err)
			}
		}
	}
	check(request("/api/v1/jobs/"+first.ID+"/download.zip", false), []string{"first", "second"})
	url := "/api/v1/jobs/" + retry.ID + "/download.zip?artifact_id=" + previous[1].ID + "&artifact_id=" + current[0].ID + "&artifact_id=" + previous[0].ID
	check(request(url, false), []string{"second", "third", "first"})
	if w := request(url, true); w.Code != 404 {
		t.Fatalf("foreign owner archive: %d", w.Code)
	}
	if w := request("/api/v1/jobs/"+retry.ID+"/download.zip?artifact_id="+unrelated[0].ID, false); w.Code != 404 {
		t.Fatalf("different source archive: %d", w.Code)
	}
	if w := request(url+"&artifact_id="+previous[0].ID, false); w.Code != 400 {
		t.Fatalf("duplicate archive ID: %d", w.Code)
	}
	if _, err := store.DB.Exec(context.Background(), `UPDATE artifacts SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, previous[0].ID); err != nil {
		t.Fatal(err)
	}
	if w := request(url, false); w.Code != 404 {
		t.Fatalf("expired archive entry: %d", w.Code)
	}
}

func TestArchiveOpensAllFilesBeforeStreamingAndKeepsReadDescriptors(t *testing.T) {
	dir := t.TempDir()
	files := make([]archiveLocation, 2)
	for i := range files {
		files[i] = archiveLocation{Path: filepath.Join(dir, archiveEntryName(i, "same.mp4")), Name: "same.mp4", Size: 8192}
		if err := os.WriteFile(files[i].Path, bytes.Repeat([]byte{byte(i)}, 8192), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w := &archiveRemovalWriter{ResponseRecorder: httptest.NewRecorder(), remove: files[1].Path}
	serveArchive(w, httptest.NewRequest("GET", "/", nil), files)
	if w.removeError != nil && runtime.GOOS != "windows" {
		t.Fatalf("could not exercise concurrent retention unlink: %v", w.removeError)
	}
	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil || len(reader.File) != 2 {
		t.Fatalf("retention interrupted open archive: %v", err)
	}
	entry, err := reader.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(entry)
	_ = entry.Close()
	if err != nil || !bytes.Equal(data, bytes.Repeat([]byte{1}, 8192)) {
		t.Fatal("removed file was not retained by its descriptor")
	}
	// Windows keeps open files locked; retention retries after the stream closes.
	if w.removeError != nil {
		if err := os.Remove(files[1].Path); err != nil {
			t.Fatal(err)
		}
	}
	missing := httptest.NewRecorder()
	serveArchive(missing, httptest.NewRequest("GET", "/", nil), files)
	if missing.Code != 404 || !strings.Contains(missing.Body.String(), "expired") || missing.Header().Get("Content-Type") == "application/zip" {
		t.Fatal("missing file produced a partial archive")
	}
}

type archiveRemovalWriter struct {
	*httptest.ResponseRecorder
	remove      string
	removeError error
}

func (w *archiveRemovalWriter) Write(p []byte) (int, error) {
	if w.remove != "" {
		w.removeError = os.Remove(w.remove)
		w.remove = ""
	}
	return w.ResponseRecorder.Write(p)
}
