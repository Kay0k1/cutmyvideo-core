package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func TestCore16HTTPExportOptionsAndRetainedRecovery(t *testing.T) {
	_ = mediaConfig(t)
	store, c, server, cookie, owner := httpPreflightFixture(t)
	path := filepath.Join(c.DataDir, "delivery-source.mp4")
	mcRun(t, context.Background(), c, "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=24", "-f", "lavfi", "-i", "sine=frequency=997:sample_rate=48000:duration=4", "-t", "4", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", path)
	source := Source{ID: newID("src"), Owner: owner, Title: "Delivery source", Kind: "upload", Path: path, DurationMS: 4000, Width: 160, Height: 96}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	c.MaxStorageBytes = 128 << 20
	c.JobTimeout = 30 * time.Second
	server.Config = c
	store.ConfigureStorage(c)
	for _, test := range []struct {
		name, format, mode string
		speed              *edit.Speed
		target             int64
		joined             bool
		codec              string
	}{
		{"speed", "mp4", "accurate", &edit.Speed{Numerator: 2, Denominator: 1}, 0, false, "aac"},
		{"reordered", "mp4", "accurate", &edit.Speed{Numerator: 3, Denominator: 2}, 0, true, "aac"},
		{"target-speed", "mp4", "accurate", &edit.Speed{Numerator: 2, Denominator: 1}, 262144, true, "aac"},
		{"wave", "wav", "accurate", nil, 0, false, "pcm_s24le"},
		{"flac", "flac", "accurate", nil, 0, false, "flac"},
		{"aac-copy", "m4a", "copy", nil, 0, false, "aac"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := requestFor(source)
			r.Format, r.CutMode, r.Quality = test.format, test.mode, "best"
			r.Speed, r.TargetSizeBytes = test.speed, test.target
			r.Ranges = []Range{{StartMS: 0, EndMS: 2000, Label: "first"}}
			if test.joined {
				r.OutputMode = "joined"
				r.Ranges = []Range{{StartMS: 2000, EndMS: 3000, Label: "later"}, {StartMS: 0, EndMS: 1000, Label: "earlier"}, {StartMS: 2000, EndMS: 3000, Label: "repeat"}}
			}
			preflight := httpPreflightReport(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, r))
			if preflight.Request.Format != test.format || preflight.Request.TargetSizeBytes != test.target {
				t.Fatalf("preflight lost export decisions: %+v", preflight.Request)
			}
			response := renderJobRequest(t, server, cookie, r, "core16-"+test.name)
			if response.Code != http.StatusAccepted {
				t.Fatal(response.Code, response.Body.String())
			}
			job, token, err := store.Claim(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			processJob(withWorkerMediaBudget(context.Background(), c), c, store, job, token)
			finished, err := store.Job(context.Background(), job.ID, owner)
			if err != nil || finished.Status != "succeeded" || len(finished.Items) != 1 || finished.Items[0].Artifact == nil {
				t.Fatalf("delivery failed: %+v %v", finished, err)
			}
			a := finished.Items[0].Artifact
			output, _, err := store.ArtifactPath(context.Background(), a.ID, owner)
			if err != nil {
				t.Fatal(err)
			}
			info, duration, err := probe(context.Background(), c, output, false)
			if err != nil || info.selectedAudio() == nil || info.selectedAudio().CodecName != test.codec {
				t.Fatal("wrong audio profile", info, err)
			}
			if edit.IsAudioOnly(test.format) && info.selectedVideo() != nil {
				t.Fatal("audio export retained video")
			}
			wantDuration := int64(2000)
			if test.joined {
				wantDuration = 3000
			}
			wantDuration, err = edit.OutputDurationMS(wantDuration, test.speed)
			if err != nil || duration < wantDuration-150 || duration > wantDuration+150 {
				t.Fatal("wrong output duration", duration, wantDuration, err)
			}
			if test.target > 0 && a.SizeBytes > test.target {
				t.Fatal("successful file exceeds the requested target", a.SizeBytes, test.target)
			}
			if test.speed != nil {
				if a.Segments == nil || len(*a.Segments) != len(r.Ranges) || (*a.Segments)[len(r.Ranges)-1].OutputEndMS != wantDuration {
					t.Fatal("speed lost output timeline", a)
				}
				var sourceElapsed int64
				for i, segment := range *a.Segments {
					start, _ := edit.OutputDurationMS(sourceElapsed, test.speed)
					sourceElapsed += r.Ranges[i].EndMS - r.Ranges[i].StartMS
					end, _ := edit.OutputDurationMS(sourceElapsed, test.speed)
					if segment.SourceStartMS != r.Ranges[i].StartMS || segment.SourceEndMS != r.Ranges[i].EndMS || segment.OutputStartMS != start || segment.OutputEndMS != end {
						t.Fatalf("wrong source/output map: %+v", segment)
					}
				}
			}
			mcRun(t, context.Background(), c, "-xerror", "-i", output, "-f", "null", "-")
			request := httptest.NewRequest(http.MethodGet, "https://example.com"+a.DownloadURL, nil)
			request.AddCookie(cookie)
			download := httptest.NewRecorder()
			server.Handler().ServeHTTP(download, request)
			if download.Code != 200 || int64(download.Body.Len()) != a.SizeBytes || (edit.IsAudioOnly(test.format) && !strings.HasPrefix(download.Header().Get("Content-Type"), "audio/")) {
				t.Fatal("wrong download bytes or audio MIME", download.Code, download.Header(), download.Body.Len())
			}
			recovered := renderJobRequest(t, server, cookie, r, "core16-"+test.name)
			if recovered.Code != http.StatusAccepted || !strings.Contains(recovered.Body.String(), job.ID) {
				t.Fatal("delivery replay changed job identity", recovered.Code, recovered.Body.String())
			}
		})
	}
	var readers, reservations int
	if err := store.DB.QueryRow(context.Background(), "SELECT count(*) FROM source_media_readers").Scan(&readers); err != nil || readers != 0 {
		t.Fatal("delivery leaked source readers", readers, err)
	}
	if err := store.DB.QueryRow(context.Background(), "SELECT count(*) FROM storage_reservations WHERE kind='job'").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatal("delivery leaked job reservations", reservations, err)
	}
}

func TestCore16ExportAdmissionRejectsOperatorTargetAndIncompatibleAudio(t *testing.T) {
	_ = mediaConfig(t)
	store, c, server, cookie, owner := httpPreflightFixture(t)
	source := addHTTPPreflightVideo(t, store, c, owner)
	server.Config.MaxOutputBytes = 1 << 20
	for _, target := range []int64{-1, 2 << 20} {
		r := requestFor(source)
		r.TargetSizeBytes = target
		assertHTTPPreflightError(t, httpPreflightRequest(t, server.Handler(), context.Background(), cookie, r), 400, "invalid_export")
		assertHTTPPreflightError(t, renderJobRequest(t, server, cookie, r, fmt.Sprint(target)), 400, "invalid_export")
	}
	for _, format := range []string{"wav", "flac", "m4a"} {
		r := requestFor(source)
		r.Format = format
		if format == "m4a" {
			r.CutMode = "copy"
		}
		response := renderJobRequest(t, server, cookie, r, format)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatal("audio-less export was queued", format, response.Code, response.Body.String())
		}
	}
	assertHTTPPreflightNoWork(t, store, 0)
}

func TestCore16VerifiedAudioDownloadMIMETypes(t *testing.T) {
	for _, test := range []struct{ name, header, mime string }{
		{"verified.flac", "fLaC\x00\x00\x00\x22", "audio/flac"},
		{"verified.m4a", "\x00\x00\x00\x20ftypM4A ", "audio/mp4"},
		{"renamed-video.m4a", "\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isommp41", "video/mp4"},
		{"verified.wav", "RF64\xff\xff\xff\xffWAVEds64", "audio/wav"},
		{"untrusted.flac", "<!DOCTYPE html><html>untrusted", "application/octet-stream"},
	} {
		path := filepath.Join(t.TempDir(), test.name)
		if err := os.WriteFile(path, []byte(test.header), 0600); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		serveFile(response, httptest.NewRequest(http.MethodGet, "/", nil), path, test.name, true)
		if response.Header().Get("Content-Type") != test.mime {
			t.Fatal(test.name, response.Header())
		}
	}
}
