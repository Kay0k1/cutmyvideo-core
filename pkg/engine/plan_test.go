package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

// Keep 1.x positional literals and comparable result/inspection contracts.
var _ = Options{"mp4", "best", "accurate"}
var _ = Range{0, 1000}
var _ = SourceInfo{1000, 160, 90, 160, 90}
var _ = Result{"file", 1, 0, 1000}
var _ = SourceInfo{} == SourceInfo{}
var _ = Result{} == Result{}

func TestPlanRoundTripActualOutputsAndPartialPublication(t *testing.T) {
	input := fixture(t)
	engine := New(Config{MaxOutputBytes: 10 << 20, FFmpegThreads: 1})
	inspected, e := engine.InspectWithTracks(context.Background(), input)
	if e != nil || len(inspected.Tracks) != 2 {
		t.Fatalf("tracks %+v %v", inspected, e)
	}
	plan := Plan{Version: 1, Input: input, Ranges: []NamedRange{{Label: "second", StartMS: 2200, EndMS: 3200}, {Label: "first", StartMS: 200, EndMS: 1000}}, Options: AdvancedOptions{Options: Options{Format: "mp4"}}}
	data, e := json.Marshal(plan)
	if e != nil {
		t.Fatal(e)
	}
	var restored Plan
	if e = json.Unmarshal(data, &restored); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	result, e := engine.ExecutePlan(context.Background(), restored, dir)
	if e != nil || len(result.Outputs) != 2 {
		t.Fatalf("separate outputs: %+v %v", result, e)
	}
	if result.Outputs[0].Label != "second" || result.Outputs[0].ActualStartMS != 2200 || len(result.Outputs[0].TrackManifest) != 2 {
		t.Fatalf("batch result: %+v", result)
	}
	joinedDir := t.TempDir()
	restored.OutputMode = "joined"
	joined, e := engine.ExecutePlan(context.Background(), restored, filepath.Join(joinedDir, "joined.mp4"))
	if e != nil || len(joined.Outputs) != 1 || len(joined.Outputs[0].Segments) != 2 || joined.Outputs[0].ActualEndMS != 1800 {
		t.Fatalf("joined outputs: %+v %v", joined, e)
	}
	// All ranges and output collisions are checked before the first publication.
	bad := plan
	bad.Ranges = append([]NamedRange(nil), plan.Ranges...)
	bad.Ranges[1].EndMS = 5000
	empty := t.TempDir()
	if _, e = engine.ExecutePlan(context.Background(), bad, empty); CodeOf(e) != CodeInvalidArgument {
		t.Fatalf("bad range: %v", e)
	}
	files, _ := os.ReadDir(empty)
	if len(files) != 0 {
		t.Fatal("invalid batch published files")
	}
	// A later filesystem failure reports the earlier durable output and leaves
	// no unpublished workspaces, allowing a caller to continue safely.
	calls := 0
	engine.publish = func(ctx context.Context, temporary, output string) (os.FileInfo, error) {
		calls++
		if calls == 2 {
			return nil, fsdurable.ErrPublicationFailed
		}
		return fsdurable.Publish(ctx, temporary, output)
	}
	partialDir := t.TempDir()
	partial, e := engine.ExecutePlan(context.Background(), plan, partialDir)
	if !errors.Is(e, fsdurable.ErrPublicationFailed) || len(partial.Outputs) != 1 {
		t.Fatalf("partial outputs: %+v %v", partial, e)
	}
	files, _ = os.ReadDir(partialDir)
	if len(files) != 1 || files[0].Name() != filepath.Base(partial.Outputs[0].Path) {
		t.Fatalf("partial cleanup: %+v", files)
	}
}
