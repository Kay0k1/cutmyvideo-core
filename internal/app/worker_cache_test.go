package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Controlled subprocess fixtures exercise the real worker, cache, output
// validation and artifact publication without contacting a video service.
func TestWorkerCachedDenialRefreshesOnceAndKeepsFreshFailuresTerminal(t *testing.T) {
	for _, outputMode := range []string{"separate", "joined"} {
		for _, tt := range []struct {
			name                            string
			cached, alwaysDenied, changedID bool
			wantStatus, wantCode            string
			wantEncodes, wantResolves       int
		}{
			{"cached-success", true, false, false, "succeeded", "", 2, 1},
			{"cached-second-denial", true, true, false, "failed", "media_upstream_denied", 2, 1},
			{"fresh-denial", false, true, false, "failed", "media_upstream_denied", 1, 1},
			{"changed-source", true, false, true, "failed", "source_changed", 1, 1},
		} {
			t.Run(outputMode+"-"+tt.name, func(t *testing.T) {
				s := testStore(t)
				c := mediaConfig(t)
				c.MaxStorageBytes, c.JobTimeout, c.SourceTimeout = 100<<20, 20*time.Second, 5*time.Second
				ctx := context.Background()
				if err := os.MkdirAll(filepath.Join(c.DataDir, "artifacts"), 0700); err != nil {
					t.Fatal(err)
				}
				source, info := storedMetadataSource(t, s)
				if tt.cached {
					if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
						t.Fatal(err)
					}
				}
				fresh := info
				if tt.changedID {
					fresh.ID = "different-source"
				}
				fresh.Formats = append([]platformFormat(nil), info.Formats...)
				fresh.Formats[0].URL = "https://media.example/fresh-video.webm?token=private-new"
				payload, err := json.Marshal(fresh)
				if err != nil {
					t.Fatal(err)
				}
				fixture := filepath.Join(c.DataDir, "fresh-metadata.json")
				if err := os.WriteFile(fixture, payload, 0600); err != nil {
					t.Fatal(err)
				}
				resolveCount := filepath.Join(c.DataDir, "resolve-count")
				c.YTDLP = filepath.Join(c.DataDir, "fixture-extractor")
				if err := os.WriteFile(c.YTDLP, []byte("#!/bin/sh\nprintf 'resolved\\n' >> "+workerFixtureQuote(resolveCount)+"\ncat "+workerFixtureQuote(fixture)+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				actualFFmpeg, err := exec.LookPath(c.FFmpeg)
				if err != nil {
					t.Fatal(err)
				}
				// The controlled encoder does not fetch the example upstream URLs.
				// Its input probe must nevertheless describe real media, and output
				// validation must continue to inspect the actual published result.
				input := filepath.Join(c.DataDir, "controlled-input.mp4")
				if _, err := runCommand(ctx, actualFFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=800:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", input); err != nil {
					t.Fatal(err)
				}
				actualProbe, err := exec.LookPath(c.FFprobe)
				if err != nil {
					t.Fatal(err)
				}
				c.FFprobe = filepath.Join(c.DataDir, "controlled-probe")
				probeScript := "#!/bin/sh\nfor value do\ncase \"$value\" in\nhttp://127.0.0.1:*) exec " + workerFixtureQuote(actualProbe) + " -v error -show_entries " + workerFixtureQuote(mediaProbeEntries) + " -show_data_hash sha256 -of json " + workerFixtureQuote(input) + ";;\nesac\ndone\nexec " + workerFixtureQuote(actualProbe) + " \"$@\"\n"
				if err := os.WriteFile(c.FFprobe, []byte(probeScript), 0700); err != nil {
					t.Fatal(err)
				}
				encodeCount := filepath.Join(c.DataDir, "encode-count")
				c.FFmpeg = filepath.Join(c.DataDir, "fixture-encoder")
				deny := "[ \"$count\" -eq 1 ]"
				if tt.alwaysDenied {
					deny = "true"
				}
				// The first controlled process denies access. A successful second
				// process creates an actual decodable H264/AAC clip for validation.
				script := "#!/bin/sh\nprintf 'encoded\\n' >> " + workerFixtureQuote(encodeCount) + "\ncount=$(wc -l < " + workerFixtureQuote(encodeCount) + ")\nif " + deny + "; then\nprintf '[http] HTTP error 403 Forbidden\\n' >&2\nexit 1\nfi\nfor value do output=$value; done\nprintf 'out_time_us=1000000\\n'\nexec " + workerFixtureQuote(actualFFmpeg) + " -v error -y -f lavfi -i 'testsrc2=size=160x90:rate=25' -f lavfi -i 'sine=frequency=800:sample_rate=48000' -t 2 -c:v libx264 -threads 1 -c:a aac -movflags +faststart \"$output\"\n"
				if err := os.WriteFile(c.FFmpeg, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				request := requestFor(source)
				request.OutputMode = outputMode
				if outputMode == "joined" {
					request.Ranges = []Range{{StartMS: 2000, EndMS: 3000, Label: "Later"}, {EndMS: 1000, Label: "Earlier"}}
				}
				if _, err := s.CreateJob(ctx, source.Owner, request, ""); err != nil {
					t.Fatal(err)
				}
				job, token, err := s.Claim(ctx)
				if err != nil {
					t.Fatal(err)
				}
				processJob(ctx, c, s, job, token)
				finished, err := s.Job(ctx, job.ID, source.Owner)
				if err != nil || finished.Status != tt.wantStatus || finished.Items[0].ErrorCode != tt.wantCode {
					t.Fatalf("wrong retry result: status/code %s/%s", finished.Status, finished.Items[0].ErrorCode)
				}
				for _, count := range []struct {
					path string
					want int
				}{{encodeCount, tt.wantEncodes}, {resolveCount, tt.wantResolves}} {
					data, err := os.ReadFile(count.path)
					if err != nil || strings.Count(string(data), "\n") != count.want {
						t.Fatal("worker repeated a terminal attempt or skipped the forced fresh lookup")
					}
				}
				if tt.wantStatus == "succeeded" {
					artifact := finished.Items[0].Artifact
					wantStart, wantEnd := int64(1000), int64(3000)
					if outputMode == "joined" {
						wantStart, wantEnd = 0, 2000
					}
					if artifact == nil || artifact.ActualStartMS != wantStart || artifact.ActualEndMS < wantEnd-50 || artifact.ActualEndMS > wantEnd+100 || finished.Items[0].ProgressMS != 2000 {
						t.Fatal("cache retry changed the requested clip or final progress")
					}
					if outputMode == "joined" && (artifact.Segments == nil || len(*artifact.Segments) != 2 || (*artifact.Segments)[0].SourceStartMS != 2000 || (*artifact.Segments)[1].SourceStartMS != 0) {
						t.Fatal("joined retry changed interval order/offsets", artifact)
					}
					path, _, err := s.ArtifactPath(ctx, artifact.ID, source.Owner)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := runCommand(ctx, actualFFmpeg, "-v", "error", "-i", path, "-f", "null", "-"); err != nil {
						t.Fatal("retried H264/AAC artifact failed full decode")
					}
				} else if finished.Items[0].Artifact != nil {
					t.Fatal("failed retry published an artifact")
				}
			})
		}
	}

}

func workerFixtureQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestWorkerReusesLocalProbeAcrossJobsAndStillVerifiesEachOutput(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout, c.SourceTimeout = 100<<20, 20*time.Second, 5*time.Second
	ctx := context.Background()
	source := workerSourceFixture(t, s, c)
	commands := countMediaCommands(t, &c)
	for range 2 {
		if _, err := s.CreateJob(ctx, source.Owner, requestFor(source), ""); err != nil {
			t.Fatal(err)
		}
		j, token, err := s.Claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		processJob(ctx, c, s, j, token)
		finished, err := s.Job(ctx, j.ID, j.Owner)
		if err != nil || finished.Status != "succeeded" || finished.Items[0].Artifact == nil {
			t.Fatalf("local export failed: %+v %v", finished, err)
		}
	}
	sourceProbes, outputProbes := 0, 0
	for _, line := range commands() {
		if !strings.HasPrefix(line, "ffprobe ") {
			continue
		}
		if strings.HasSuffix(line, " "+source.Path) {
			sourceProbes++
		} else {
			outputProbes++
		}
	}
	if sourceProbes != 1 || outputProbes != 2 {
		t.Fatal("source reuse or independent output verification changed", sourceProbes, outputProbes)
	}
}

func TestWorkerRepeatedLocalRangesReuseOwnedPublicationWithIndependentFiles(t *testing.T) {
	s := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.JobTimeout, c.SourceTimeout = 100<<20, 20*time.Second, 5*time.Second
	ctx := context.Background()
	source := workerSourceFixture(t, s, c)
	actualEncoder, err := exec.LookPath(c.FFmpeg)
	if err != nil {
		t.Fatal(err)
	}
	commands := countMediaCommands(t, &c)
	request := requestFor(source)
	request.Ranges = []Range{{StartMS: 1000, EndMS: 3000, Label: "First"}, {StartMS: 1000, EndMS: 3000, Label: "Again"}}
	if _, err := s.CreateJob(ctx, source.Owner, request, ""); err != nil {
		t.Fatal(err)
	}
	j, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	processJob(ctx, c, s, j, token)
	finished, err := s.Job(ctx, j.ID, j.Owner)
	if err != nil || finished.Status != "succeeded" || len(finished.Items) != 2 || finished.Items[0].Artifact == nil || finished.Items[1].Artifact == nil {
		t.Fatalf("repeated local batch failed: %+v %v", finished, err)
	}
	encodes := 0
	for _, line := range commands() {
		if strings.HasPrefix(line, "ffmpeg ") {
			encodes++
		}
	}
	if encodes != 1 {
		t.Fatal("repeated range was encoded again", encodes)
	}
	first, _, err := s.ArtifactPath(ctx, finished.Items[0].Artifact.ID, j.Owner)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.ArtifactPath(ctx, finished.Items[1].Artifact.ID, j.Owner)
	if err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil || os.SameFile(firstInfo, secondInfo) || firstInfo.Size() != secondInfo.Size() || finished.Items[0].Artifact.ActualStartMS != finished.Items[1].Artifact.ActualStartMS || finished.Items[0].Artifact.ActualEndMS != finished.Items[1].Artifact.ActualEndMS || finished.Items[1].ProgressMS != 2000 {
		t.Fatal("duplicate result shared its file or changed bounds", err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil || string(firstBytes) != string(secondBytes) {
		t.Fatal("duplicate output bytes changed", err)
	}
	if _, err := runCommand(ctx, actualEncoder, "-v", "error", "-i", second, "-f", "null", "-"); err != nil {
		t.Fatal("reused result failed independent decode", err)
	}
}
