//go:build !windows

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCanceledWindowReleasesDecoderSourceAndDiskReservation(t *testing.T) {
	for _, storyboard := range []bool{false, true} {
		t.Run(fmt.Sprint(storyboard), func(t *testing.T) {
			store := testStore(t)
			c := mediaConfig(t)
			c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
			source := Source{ID: newID("src"), Owner: "owner", Title: "Canceled window", Kind: "upload", DurationMS: 4000, Path: previewFixture(t, c)}
			if err := store.AddSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(c.DataDir, "render-started")
			script := filepath.Join(c.DataDir, "blocking-render")
			if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\nprintf 'started' > '%s'\nsleep 30\n", marker)), 0700); err != nil {
				t.Fatal(err)
			}
			c.FFmpeg = script
			app := NewServer(c, store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, "/preview?start_ms=0", nil).WithContext(ctx)
			r.SetPathValue("id", source.ID)
			done := make(chan struct{})
			go func() {
				defer close(done)
				app.sourceWindowWithPublisher(httptest.NewRecorder(), r, source.Owner, storyboard, store.publishPreview)
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("renderer never started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			started := time.Now()
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("canceled window retained decoder")
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("cancellation took %s", elapsed)
			}
			var count int
			if err := store.DB.QueryRow(context.Background(), "SELECT count(*) FROM storage_reservations WHERE kind IN ('preview','preview_cache')").Scan(&count); err != nil || count != 0 {
				t.Fatalf("cancellation leaked reservation: %d %v", count, err)
			}
			entries, err := os.ReadDir(filepath.Join(c.DataDir, "previews"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("cancellation leaked files: %v %v", entries, err)
			}
			if _, err := store.DeleteSource(context.Background(), source.ID, source.Owner); err != nil {
				t.Fatalf("cancellation retained source pin: %v", err)
			}
		})
	}
}
