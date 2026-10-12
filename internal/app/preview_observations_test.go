package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in observations keep the media/host identical across baseline and
// candidate revisions. Every cold observation has a new cache identity; its
// immediately following warm observation reuses that canonical window.
func TestPreviewObservations(t *testing.T) {
	input, output := os.Getenv("CUTMY_PREVIEW_OBSERVATION_SOURCE"), os.Getenv("CUTMY_PREVIEW_OBSERVATION_OUTPUT")
	if input == "" || output == "" {
		t.Skip("set observation source and output to collect controlled preview timings")
	}
	store := testStore(t)
	c := mediaConfig(t)
	c.FFmpegThreads = 2
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	inspected, err := InspectLocal(context.Background(), c, input)
	if err != nil {
		t.Fatal(err)
	}
	app := NewServer(c, store)
	type observation struct {
		ColdMS float64 `json:"cold_ms"`
		WarmMS float64 `json:"warm_ms"`
		Bytes  int     `json:"bytes"`
	}
	observations := make([]observation, 0, 5)
	for index := 0; index < 5; index++ {
		source := inspected
		source.ID, source.Owner, source.Title = newID("src"), "preview-observations", "1080p60 controlled media"
		if err := store.AddSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
		entry := observation{}
		for attempt := 0; attempt < 2; attempt++ {
			request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sources/%s/preview?start_ms=1250", source.ID), nil)
			request.SetPathValue("id", source.ID)
			response := httptest.NewRecorder()
			started := time.Now()
			app.sourcePreview(response, request, source.Owner)
			elapsed := float64(time.Since(started).Microseconds()) / 1000
			if response.Code != 200 {
				t.Fatalf("observation %d/%d: %d %s", index, attempt, response.Code, response.Body.String())
			}
			if attempt == 0 {
				if response.Header().Get("X-Preview-Cache") != "miss" {
					t.Fatal("cold observation reused cache")
				}
				entry.ColdMS, entry.Bytes = elapsed, response.Body.Len()
				// Keep one independently decodable output as evidence.
				if index == 0 {
					if err := os.WriteFile(filepath.Join(filepath.Dir(output), filepath.Base(output)+".mp4"), response.Body.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if response.Header().Get("X-Preview-Cache") != "hit" {
					t.Fatal("warm observation rendered again")
				}
				entry.WarmMS = elapsed
			}
		}
		observations = append(observations, entry)
	}
	data, err := json.MarshalIndent(map[string]any{"source": input, "video": "1920x1080 60fps H.264", "window_ms": previewWindowMS, "decoder_threads": c.FFmpegThreads, "filter_threads": 1, "encoder_threads": 2, "method": "HTTP handler to complete playable MP4 (includes durable publication and cache lookup)", "observations": observations}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
