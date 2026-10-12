package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func countMediaCommands(t *testing.T, c *Config) func() []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("command witness uses the POSIX shell; media behavior has native checks")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	for _, tool := range []*string{&c.FFprobe, &c.FFmpeg} {
		path, err := exec.LookPath(*tool)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		wrapper := filepath.Join(dir, name)
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' %s \"$*\" >> %s\nexec %s \"$@\"\n", quote(name), quote(log), quote(path))
		if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		*tool = wrapper
	}
	return func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func TestLocalExportSessionReusesOriginalAndVerifiesEveryJoinedResult(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := batchFixture(t, c, ctx)
	commands := countMediaCommands(t, &c)
	ctx, session := BeginLocalExport(ctx, input)
	session.SetWorkspace(c.DataDir)
	defer session.Close()
	if _, err := InspectLocal(ctx, c, input); err != nil {
		t.Fatal(err)
	}
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate"}
	ranges := []Range{{StartMS: 3200, EndMS: 4600}, {StartMS: 400, EndMS: 1600}}
	var outputs []string
	for i := range 2 {
		out := filepath.Join(c.DataDir, fmt.Sprintf("joined-%d.mp4", i))
		if _, err := ExportJoinedLocal(ctx, c, input, out, ranges, request); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, out)
	}
	probes := make(map[string]int)
	for _, line := range commands() {
		if strings.HasPrefix(line, "ffprobe ") {
			fields := strings.Fields(line)
			probes[fields[len(fields)-1]]++
		}
	}
	if probes[input] != 1 || probes[outputs[0]] != 1 || probes[outputs[1]] != 1 || len(probes) != 3 {
		t.Fatalf("source reuse or required final verification changed: %+v", probes)
	}
	// Outputs cannot inherit the source cache, including when a previously
	// verified result is damaged later in the same operation.
	if err := os.WriteFile(outputs[0], []byte("broken output"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := probeStreams(ctx, c, outputs[0], false); err == nil {
		t.Fatal("damaged output reused an earlier probe")
	}
}

func TestLocalExportSessionExtractsSubtitlesOnceAndClipsEveryResult(t *testing.T) {
	for _, mode := range []string{"accurate", "copy"} {
		t.Run(mode, func(t *testing.T) { testSessionSubtitles(t, mode) })
	}
}

func testSessionSubtitles(t *testing.T, mode string) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := batchFixture(t, c, ctx)
	commands := countMediaCommands(t, &c)
	ctx, session := BeginLocalExport(ctx, input)
	session.SetWorkspace(c.DataDir)
	defer session.Close()
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: mode, SubtitleMode: "preserve"}
	for i, r := range []Range{{StartMS: 3600, EndMS: 4600}, {StartMS: 1200, EndMS: 1800}, {StartMS: 1500, EndMS: 2400}} {
		out := filepath.Join(c.DataDir, fmt.Sprintf("clip-%d.mp4", i))
		start, end, err := ExportLocal(ctx, c, input, out, r, request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := runCommand(ctx, "ffmpeg", "-v", "error", "-i", out, "-map", "0:s:0", "-c:s", "srt", "-f", "srt", "-")
		if err != nil {
			t.Fatal(err)
		}
		cues, err := parseSRT(data)
		if err != nil {
			t.Fatal(err)
		}
		text := "Alpha"
		cueStart, cueEnd := int64(500), int64(2800)
		if i == 0 {
			text = "Beta"
			cueStart, cueEnd = 3200, 5500
		}
		found := false
		for _, cue := range cues {
			if strings.Contains(cue.Text, text) {
				found = true
				wantStart, wantEnd := max(cueStart, start)-start, min(cueEnd, end)-start
				if cue.StartMS < wantStart-2 || cue.StartMS > wantStart+2 || cue.EndMS < wantEnd-2 || cue.EndMS > wantEnd+2 {
					t.Fatalf("spanning cue was not clipped to actual bounds: %+v, %d-%d", cue, start, end)
				}
			}
		}
		if !found {
			t.Fatalf("missing %s cue: %s", text, data)
		}
	}
	var sourceProbes, extractions int
	for _, line := range commands() {
		if strings.HasPrefix(line, "ffprobe ") && strings.HasSuffix(line, " "+input) && !strings.Contains(line, "-read_intervals") {
			sourceProbes++
		}
		if strings.HasPrefix(line, "ffmpeg ") && strings.Contains(line, "-i "+input+" -map 0:3 -c:s srt -f srt -") {
			extractions++
		}
	}
	if sourceProbes != 1 || extractions != 1 {
		t.Fatalf("source probed %d times and track extracted %d times", sourceProbes, extractions)
	}
	dir := session.cueDir
	if dir == "" {
		t.Fatal("source subtitle copy was not staged")
	}
	session.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("subtitle workspace survived session close: %v", err)
	}
}

func TestLocalExportSessionRejectsChangedSourceAndDoesNotCacheStages(t *testing.T) {
	c := mediaConfig(t)
	ctx := context.Background()
	input := batchFixture(t, c, ctx)
	stage := filepath.Join(c.DataDir, "stage.mp4")
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, data, 0600); err != nil {
		t.Fatal(err)
	}
	commands := countMediaCommands(t, &c)
	ctx, session := BeginLocalExport(ctx, input)
	defer session.Close()
	for range 2 {
		if _, err := probeStreams(ctx, c, input, false); err != nil {
			t.Fatal(err)
		}
		if _, err := probeStreams(ctx, c, stage, false); err != nil {
			t.Fatal(err)
		}
	}
	var stages int
	for _, line := range commands() {
		if strings.HasSuffix(line, " "+stage) {
			stages++
		}
	}
	if stages != 2 || localExportSession(ctx, input, true) != nil {
		t.Fatal("staged or remote inputs inherited the original source cache")
	}
	if err := os.Rename(stage, input); err != nil {
		t.Fatal(err)
	}
	if _, err := probeStreams(ctx, c, input, false); exportProblem(err).code != "source_changed" {
		t.Fatalf("changed inode reused its original metadata: %v", err)
	}
}

func TestWorkerSubtitleCacheRemainsChargedUntilFinalUnfinishedPublication(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovered-%t", recovered), func(t *testing.T) {
			store := testStore(t)
			c := mediaConfig(t)
			c.MaxStorageBytes, c.MaxOwnerBytes = 2<<30, 2<<30
			c.JobTimeout, c.SourceTimeout, c.ArtifactTTL = 30*time.Second, 15*time.Second, time.Hour
			store.ConfigureStorage(c)
			ctx := context.Background()
			input := batchFixture(t, c, ctx)
			source := Source{ID: newID("src"), Owner: "subtitle-cache-owner", Kind: "upload", Path: input, DurationMS: 6000}
			if err := store.AddSource(ctx, source); err != nil {
				t.Fatal(err)
			}
			request := ExportRequest{SourceID: source.ID, Format: "mp4", Quality: "best", CutMode: "copy", SubtitleMode: "preserve", Ranges: []Range{{StartMS: 3200, EndMS: 4600}, {StartMS: 1200, EndMS: 1800}}}
			if recovered {
				request.Ranges = append(request.Ranges, Range{StartMS: 400, EndMS: 1600})
			}
			if _, err := store.CreateJob(ctx, source.Owner, request, ""); err != nil {
				t.Fatal(err)
			}
			job, token, err := store.Claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if recovered {
				last := len(job.Items) - 1
				path := filepath.Join(c.DataDir, "already-ready.mp4")
				start, end, err := ExportLocal(ctx, c, input, path, request.Ranges[last], request)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				artifact := Artifact{ID: newID("art"), Filename: "already-ready.mp4", SizeBytes: info.Size(), ActualStartMS: start, ActualEndMS: end}
				job.Items[last].Status, job.Items[last].Artifact = "succeeded", &artifact
				if err := store.PublishArtifact(ctx, job, path, token, artifact); err != nil {
					t.Fatal(err)
				}
				if _, err := store.DB.Exec(ctx, "UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", job.ID); err != nil {
					t.Fatal(err)
				}
				job, token, err = store.Claim(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			commands := countMediaCommands(t, &c)
			publications := 0
			_, initialReserve := ledgerBytes(t, store)
			var publishedCeilings int64
			processJobWithPublisher(ctx, c, store, job, token, func(ctx context.Context, snapshot Job, path, token string, artifact Artifact) error {
				publications++
				unfinished := 0
				for _, item := range snapshot.Items {
					if item.Status != "succeeded" || item.Artifact == nil {
						unfinished++
					}
				}
				caches, err := filepath.Glob(filepath.Join(c.DataDir, "work", job.ID+"-"+token, ".source-subtitles-*"))
				if err != nil || unfinished > 0 && len(caches) != 1 || unfinished == 0 && len(caches) != 0 {
					t.Fatalf("cache lifetime disagrees with unfinished work: %d files for %d pending outputs: %v", len(caches), unfinished, err)
				}
				if err := store.PublishArtifact(ctx, snapshot, path, token, artifact); err != nil {
					return err
				}
				for _, item := range snapshot.Items {
					if item.Artifact != nil && item.Artifact.ID == artifact.ID {
						publishedCeilings += outputBudget(c, Range{StartMS: item.StartMS, EndMS: item.EndMS}, snapshot.Request)
					}
				}
				// Publication releases its final-output ceiling. Work headroom
				// remains until the worker's fenced workspace cleanup succeeds.
				expected := initialReserve - publishedCeilings
				_, reserved := ledgerBytes(t, store)
				if reserved != expected || unfinished > 0 && reserved < subtitleTrackAllowance {
					t.Fatalf("shared cache released early or retained after removal: %d expected %d, pending %d", reserved, expected, unfinished)
				}
				return nil
			})
			finished, err := store.Job(ctx, job.ID, source.Owner)
			if err != nil || finished.Status != "succeeded" || publications != 2 {
				t.Fatalf("fresh/recovered batch failed: %+v, publications=%d, %v", finished, publications, err)
			}
			_, reserved := ledgerBytes(t, store)
			_, workErr := os.Stat(filepath.Join(c.DataDir, "work", job.ID+"-"+token))
			if reserved != 0 || !os.IsNotExist(workErr) {
				t.Fatalf("completed workspace/charge not released together: reserved=%d, work=%v", reserved, workErr)
			}
			var probes, extracts int
			for _, line := range commands() {
				if strings.HasPrefix(line, "ffprobe ") && strings.HasSuffix(line, " "+input) && !strings.Contains(line, "-read_intervals") {
					probes++
				}
				if strings.Contains(line, "-i "+input+" -map 0:3 -c:s srt -f srt -") {
					extracts++
				}
			}
			if probes != 1 || extracts != 1 {
				t.Fatalf("worker repeated source work: %d probes, %d extracts", probes, extracts)
			}
		})
	}
}
