package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCutListPlanRetainsEditableOrderAndMarkers(t *testing.T) {
	list, err := ParseCutList("csv", []byte("5,6,later\n1,2,earlier\n8,,last chapter\n"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildCutListPlan("source.mp4", list, 10000, AdvancedOptions{}, "joined")
	if err != nil || plan.Version != 1 || plan.Input != "source.mp4" || plan.OutputMode != "joined" || len(plan.Ranges) != 3 || plan.Ranges[0].StartMS != 5000 || plan.Ranges[1].StartMS != 1000 || plan.Ranges[2].EndMS != 10000 || plan.Options.Format != "mp4" {
		t.Fatal(plan, err)
	}
	if _, err := BuildCutListPlan("source.mp4", list, 0, AdvancedOptions{}, "joined"); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("open final marker must be corrective", err)
	}
	if _, err := BuildCutListPlan("source.mp4", list, 10000, AdvancedOptions{}, "other"); CodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	converted, err := CutListFromPlan(plan)
	if err != nil || *converted.Entries[0].EndMS != 6000 || converted.Entries[1].StartMS != 1000 || converted.Entries[2].EndMS == nil {
		t.Fatal(converted, err)
	}
	if _, err := EncodeCutList("ffmetadata", converted); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("arbitrary source ordering cannot become chapter ordering", err)
	}
}

func TestCutListInterchangeTypedRefusals(t *testing.T) {
	if _, err := ParseCutList("unknown", []byte("0,1,test\n")); CodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	if _, err := CutListFromPlan(Plan{Version: 2}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	if _, err := CutListFromSegments(nil); CodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
}

func TestCutListChaptersAcceptedByIndependentFFmpeg(t *testing.T) {
	input := fixture(t)
	list, err := ParseCutList("csv", []byte("0.5,2,\"Глава = первая; #1\"\n2,4,Second\n"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := EncodeCutList("ffmetadata", list)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	metadataPath, output := filepath.Join(dir, "chapters.txt"), filepath.Join(dir, "chapters.mp4")
	if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if raw, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", input, "-f", "ffmetadata", "-i", metadataPath, "-map", "0", "-map_metadata", "1", "-map_chapters", "1", "-c", "copy", output).CombinedOutput(); err != nil {
		t.Fatalf("independent chapter mux: %v %s", err, raw)
	}
	raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_chapters", "-of", "json", output).Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Chapters []struct {
			Start string `json:"start_time"`
			End   string `json:"end_time"`
			Tags  struct {
				Title string `json:"title"`
			} `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(raw, &report); err != nil || len(report.Chapters) != 2 {
		t.Fatal(string(raw), err)
	}
	if report.Chapters[0].Start != "0.500000" || report.Chapters[0].End != "2.000000" || report.Chapters[0].Tags.Title != "Глава = первая; #1" || report.Chapters[1].End != "4.000000" {
		t.Fatal(string(raw))
	}
}
