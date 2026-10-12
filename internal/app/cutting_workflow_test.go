package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfiguredCuttingLimitsAtBoundaryAndOneAbove(t *testing.T) {
	for _, limit := range []int{1, 32, 128} {
		c := Config{MaxRanges: limit, MaxRangeMS: 12 * 60 * 60 * 1000, MaxJobMS: 24 * 60 * 60 * 1000}
		source := Source{DurationMS: c.MaxJobMS + 1}
		validate := func(ranges []Range, message string) {
			t.Helper()
			request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", Ranges: ranges}
			err := request.Validate(c, source)
			if message == "" && err != nil || message != "" && (err == nil || !strings.Contains(err.Error(), message)) {
				t.Fatalf("limit=%d ranges=%+v expected diagnostic %q: %v", limit, ranges, message, err)
			}
		}
		ranges := make([]Range, limit)
		for i := range ranges {
			ranges[i] = Range{StartMS: int64(i), EndMS: int64(i + 1)}
		}
		validate(ranges, "")
		validate(append(ranges, Range{EndMS: 1}), fmt.Sprintf("choose between 1 and %d ranges (received %d)", limit, limit+1))
		validate(nil, "mark a start and an end")
		validate([]Range{{EndMS: c.MaxRangeMS}}, "")
		validate([]Range{{EndMS: c.MaxRangeMS + 1}}, "shorten the range")
		validate([]Range{{EndMS: source.DurationMS + 1}}, "source duration")
		if limit >= 3 {
			validate([]Range{{EndMS: c.MaxRangeMS}, {EndMS: c.MaxRangeMS}}, "")
			validate([]Range{{EndMS: c.MaxRangeMS}, {EndMS: c.MaxRangeMS}, {EndMS: 1}}, "total selected duration")
		}
	}
}

// This controlled HTTPS response exercises direct-link import through the real
// source publication, HTTP job admission, worker and private download paths.
// Network address restrictions have separate tests; no public service is needed.
func TestDirectLinkCuttingExportsBeginningEndAndSelectedAudio(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.SourceTimeout, c.JobTimeout = 20*time.Second, time.Minute
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	c.MaxActiveJobs = 32
	c.SourceTTL, c.ArtifactTTL, c.StorageWaitTimeout = time.Hour, time.Hour, time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fixture := filepath.Join(c.DataDir, "direct.mp4")
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "color=red:size=160x96:rate=10:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=4",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-c:v", "libx264", "-threads", "1", "-g", "10", "-bf", "0",
		"-c:a", "aac", "-metadata:s:a:1", "language=rus", "-disposition:a:0", "default", "-disposition:a:1", "0", fixture)
	media, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(c, s)
	imported := httptest.NewRecorder()
	server.addSourceWithClient(imported, httptest.NewRequest(http.MethodPost, "/api/v1/sources", strings.NewReader(`{"url":"https://media.example/fixture.mp4"}`)), "owner", func() *http.Client {
		return &http.Client{Transport: networkTestTransport(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://media.example/fixture.mp4" {
				t.Fatal("direct source URL changed", request.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(media)), Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(bytes.NewReader(media))}, nil
		})}
	})
	if imported.Code != http.StatusCreated {
		t.Fatal("direct import failed", imported.Code, imported.Body.String())
	}
	var source Source
	if err := json.Unmarshal(imported.Body.Bytes(), &source); err != nil || source.Kind != "direct" || source.DurationMS < 3900 {
		t.Fatal("direct import metadata", source, err)
	}
	audio := 2
	for _, variant := range []struct{ mode, format string }{{"accurate", "mp4"}, {"copy", "mp4"}, {"accurate", "mp3"}} {
		t.Run(variant.mode+"-"+variant.format, func(t *testing.T) {
			request := ExportRequest{SourceID: source.ID, Format: variant.format, Quality: "best", CutMode: variant.mode, AudioTrack: &audio,
				Ranges: []Range{{EndMS: 1500, Label: "Beginning"}, {StartMS: 2500, EndMS: 4000, Label: "End"}}}
			body, _ := json.Marshal(request)
			post := func() Job {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body))
				r.Header.Set("Idempotency-Key", variant.mode+variant.format)
				response := httptest.NewRecorder()
				server.createJob(response, r, "owner")
				var job Job
				if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &job) != nil {
					t.Fatal("direct job rejected", response.Code, response.Body.String())
				}
				return job
			}
			accepted := post()
			if replay := post(); replay.ID != accepted.ID {
				t.Fatal("direct job retry duplicated export")
			}
			job, token, err := s.Claim(ctx)
			if err != nil || job.ID != accepted.ID {
				t.Fatal("direct job claim", job.ID, err)
			}
			processJob(ctx, c, s, job, token)
			finished, err := s.Job(ctx, accepted.ID, "owner")
			if err != nil || finished.Status != "succeeded" || len(finished.Items) != 2 {
				t.Fatal("direct cutting failed", finished, err)
			}
			for i, item := range finished.Items {
				artifact := item.Artifact
				if artifact == nil || item.Label != request.Ranges[i].Label {
					t.Fatal("direct result lost interval or label", item)
				}
				r := httptest.NewRequest(http.MethodGet, artifact.DownloadURL, nil)
				r.SetPathValue("id", artifact.ID)
				response := httptest.NewRecorder()
				server.download(response, r, "owner")
				if response.Code != http.StatusOK || int64(response.Body.Len()) != artifact.SizeBytes {
					t.Fatal("direct download failed", response.Code, response.Body.String())
				}
				path := filepath.Join(t.TempDir(), "download."+variant.format)
				if err := os.WriteFile(path, response.Body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				mcRun(t, ctx, c, "-xerror", "-err_detect", "explode", "-i", path, "-f", "null", "-")
				streams := mcProbe(t, ctx, c, path)
				mcTone(t, mcPCM(t, ctx, c, path, .3, .2, mcAudioIndex(t, streams)), 880)
				if variant.mode == "accurate" && artifact.ActualStartMS != request.Ranges[i].StartMS || artifact.ActualEndMS < request.Ranges[i].EndMS-100 || artifact.ActualEndMS > request.Ranges[i].EndMS+100 {
					t.Fatal("direct result bounds mismatch", artifact)
				}
				if _, _, err := s.ArtifactPath(ctx, artifact.ID, "another-owner"); err != ErrNotFound {
					t.Fatal("direct export leaked to another owner", err)
				}
			}
		})
	}
}
