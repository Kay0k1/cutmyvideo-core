package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

func collectionFixture(t *testing.T) CollectionPlan {
	t.Helper()
	return CollectionPlan{Version: 1, Input: fixture(t), Clips: []CollectionClip{
		{ID: "highlight", Label: "Later then earlier", Ranges: []CollectionRange{{Label: "answer", StartMS: 2200, EndMS: 3000}, {Label: "question", StartMS: 200, EndMS: 1000}}},
		{ID: "vertical", Label: "Vertical", Ranges: []CollectionRange{{StartMS: 1000, EndMS: 2000}}, Options: CollectionOptions{Crop: &Crop{X: 0.25, Y: 0, Width: 0.5, Height: 1}}},
	}}
}

func TestCollectionRoundTripSelectedDraftAndNoOverwrite(t *testing.T) {
	plan := collectionFixture(t)
	before, _ := json.Marshal(plan)
	var roundtrip CollectionPlan
	if err := json.Unmarshal(before, &roundtrip); err != nil {
		t.Fatal(err)
	}
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 10 << 20})
	dir := t.TempDir()
	checked, err := e.PreflightCollection(context.Background(), roundtrip, dir, CollectionExecution{})
	if err != nil || !checked.Valid || len(checked.Clips) != 2 || checked.Clips[0].RequestedMS != 1600 || checked.Clips[1].Video.Width != 80 {
		t.Fatalf("preflight %+v %v", checked, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("validation wrote files")
	}
	final, err := e.ExecuteCollection(context.Background(), roundtrip, dir, CollectionExecution{})
	if err != nil || len(final.Outputs) != 2 {
		t.Fatalf("final %+v %v", final, err)
	}
	first := final.Outputs[0]
	if first.ID != "highlight" || filepath.Base(first.Path) != "highlight.final.mp4" || first.ActualStartMS != 0 || first.ActualEndMS != 1600 || len(first.Segments) != 2 || first.Segments[0].SourceStartMS != 2200 || first.Segments[1].SourceStartMS != 200 || first.Segments[1].OutputStartMS != 800 || len(first.TrackManifest) != 2 {
		t.Fatalf("manifest %+v", first)
	}
	decodeCollectionMedia(t, first.Path)
	verticalInfo, err := e.Inspect(context.Background(), final.Outputs[1].Path)
	if err != nil || verticalInfo.Width != 80 || verticalInfo.Height != 90 {
		t.Fatalf("per clip crop %+v %v", verticalInfo, err)
	}
	draft, err := e.ExecuteCollection(context.Background(), roundtrip, dir, CollectionExecution{Rendition: "draft"})
	if err != nil || len(draft.Outputs) != 2 || draft.Rendition != "draft" {
		t.Fatalf("draft %+v %v", draft, err)
	}
	if !reflect.DeepEqual(draft.Outputs[0].Segments, first.Segments) || filepath.Base(draft.Outputs[0].Path) != "highlight.draft.mp4" {
		t.Fatalf("draft edits differ %+v", draft)
	}
	finalBytes, _ := os.ReadFile(first.Path)
	draftBytes, _ := os.ReadFile(draft.Outputs[0].Path)
	if bytes.Equal(finalBytes, draftBytes) || len(draftBytes) >= len(finalBytes) {
		t.Fatal("draft did not use lower encode quality on small input")
	}
	draftVertical, err := e.Inspect(context.Background(), draft.Outputs[1].Path)
	if err != nil || draftVertical.Width != verticalInfo.Width || draftVertical.Height != verticalInfo.Height {
		t.Fatalf("draft geometry changed %+v %v", draftVertical, err)
	}
	decodeCollectionMedia(t, draft.Outputs[0].Path)
	// Stable IDs keep filenames independent of titles, selection and position.
	roundtrip.Clips[0].Label = "Renamed"
	roundtrip.Clips[0].Ranges[0].StartMS = 2400
	selectedDir := t.TempDir()
	selected, err := e.ExecuteCollection(context.Background(), roundtrip, selectedDir, CollectionExecution{ClipIDs: []string{"highlight"}})
	if err != nil || len(selected.Outputs) != 1 || selected.Outputs[0].ID != "highlight" || selected.Outputs[0].ActualEndMS != 1400 || filepath.Base(selected.Outputs[0].Path) != "highlight.final.mp4" {
		t.Fatalf("selected %+v %v", selected, err)
	}
	if _, err := os.Stat(filepath.Join(selectedDir, "vertical.final.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unselected clip exported")
	}
	if _, err = e.ExecuteCollection(context.Background(), plan, dir, CollectionExecution{}); CodeOf(err) != CodeOutputExists || !errors.Is(err, os.ErrExist) {
		t.Fatal("existing outputs accepted", err)
	}
	unchanged, _ := os.ReadFile(first.Path)
	if !bytes.Equal(finalBytes, unchanged) {
		t.Fatal("existing output changed")
	}
	after, _ := json.Marshal(plan)
	if !bytes.Equal(before, after) {
		t.Fatal("reusable plan mutated")
	}
}

func TestCollectionInvalidSelectedAndGlobalShape(t *testing.T) {
	plan := collectionFixture(t)
	e := New(Config{FFmpegThreads: 1})
	for _, tc := range []struct {
		name, id, field string
		change          func(*CollectionPlan, *CollectionExecution)
	}{
		{"version", "", "version", func(p *CollectionPlan, _ *CollectionExecution) { p.Version = 2 }},
		{"unsafe-id", "../outside", "clips[0].id", func(p *CollectionPlan, _ *CollectionExecution) { p.Clips[0].ID = "../outside" }},
		{"reserved-id", "con", "clips[0].id", func(p *CollectionPlan, _ *CollectionExecution) { p.Clips[0].ID = "con" }},
		{"reserved-unselected-id", "lpt9", "clips[1].id", func(p *CollectionPlan, o *CollectionExecution) {
			p.Clips[1].ID = "lpt9"
			o.ClipIDs = []string{"highlight"}
		}},
		{"duplicate", "highlight", "id", func(p *CollectionPlan, _ *CollectionExecution) { p.Clips[1].ID = "highlight" }},
		{"unknown-selection", "missing", "clip_ids[0]", func(_ *CollectionPlan, o *CollectionExecution) { o.ClipIDs = []string{"missing"} }},
		{"range", "highlight", "ranges[0].end_ms", func(p *CollectionPlan, _ *CollectionExecution) { p.Clips[0].Ranges[0].EndMS = 5000 }},
		{"quality", "highlight", "options.quality", func(p *CollectionPlan, _ *CollectionExecution) { p.Clips[0].Options.Quality = "typo" }},
		{"draft-copy", "highlight", "options.cut_mode", func(p *CollectionPlan, o *CollectionExecution) {
			p.Clips[0].Options.CutMode = "copy"
			o.Rendition = "draft"
		}},
		{"draft-mp3", "highlight", "options.format", func(p *CollectionPlan, o *CollectionExecution) {
			p.Clips[0].Options.Format = "mp3"
			o.Rendition = "draft"
		}},
		{"audio", "highlight", "options.audio_track", func(p *CollectionPlan, _ *CollectionExecution) { index := 99; p.Clips[0].Options.AudioTrack = &index }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob, _ := json.Marshal(plan)
			var p CollectionPlan
			_ = json.Unmarshal(blob, &p)
			o := CollectionExecution{}
			tc.change(&p, &o)
			dir := t.TempDir()
			_, err := e.PreflightCollection(context.Background(), p, dir, o)
			var fieldErr *CollectionFieldError
			if !errors.As(err, &fieldErr) || fieldErr.ClipID != tc.id || fieldErr.Field != tc.field {
				t.Fatalf("location %+v %v", fieldErr, err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("invalid plan wrote files")
			}
		})
	}
	plan.Clips[1].Ranges[0].EndMS = 9000
	plan.Clips[1].Options.Quality = "unfinished"
	if _, err := e.PreflightCollection(context.Background(), plan, t.TempDir(), CollectionExecution{ClipIDs: []string{"highlight"}}); err != nil {
		t.Fatal("unfinished unselected edit blocked selection", err)
	}
	// Selected outputs are all checked before the first render.
	plan.Clips[1].Ranges[0].EndMS = 2000
	plan.Clips[1].Options.Quality = ""
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vertical.final.mp4"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := e.ExecuteCollection(context.Background(), plan, dir, CollectionExecution{})
	if CodeOf(err) != CodeOutputExists || len(result.Outputs) != 0 {
		t.Fatalf("collision rendered early output %+v %v", result, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("collision wrote files")
	}
}

func TestCollectionPartialPublicationAndAggregateLimit(t *testing.T) {
	plan := collectionFixture(t)
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 10 << 20})
	calls := 0
	e.publish = func(ctx context.Context, temporary, output string) (os.FileInfo, error) {
		calls++
		if calls == 2 {
			return nil, fsdurable.ErrPublicationFailed
		}
		return fsdurable.Publish(ctx, temporary, output)
	}
	dir := t.TempDir()
	partial, err := e.ExecuteCollection(context.Background(), plan, dir, CollectionExecution{})
	if CodeOf(err) != CodePublicationFailed || len(partial.Outputs) != 1 || partial.Outputs[0].ID != "highlight" {
		t.Fatalf("partial %+v %v", partial, err)
	}
	decodeCollectionMedia(t, partial.Outputs[0].Path)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("workspace leaked %+v", entries)
	}
	// Use identical clips so independent whole-operation byte accounting is
	// distinguishable from merely resetting the original per-clip budget.
	plan.Clips[1] = plan.Clips[0]
	plan.Clips[1].ID = "repeat"
	max := partial.Outputs[0].SizeBytes + partial.Outputs[0].SizeBytes/3
	e = New(Config{FFmpegThreads: 1, MaxOutputBytes: max})
	dir = t.TempDir()
	partial, err = e.ExecuteCollection(context.Background(), plan, dir, CollectionExecution{})
	if CodeOf(err) != CodeOutputLimit || len(partial.Outputs) != 1 {
		t.Fatalf("aggregate bytes not bounded %+v %v (limit=%d)", partial, err, max)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("byte limit leaked %+v", entries)
	}
	// Aggregate range and duration budgets also span clip boundaries.
	e = New(Config{})
	e.config.MaxRanges = 2
	if _, err = e.PreflightCollection(context.Background(), plan, t.TempDir(), CollectionExecution{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("aggregate range budget reset", err)
	}
	e.config.MaxRanges = 128
	e.config.MaxJobMS = 2000
	if _, err = e.PreflightCollection(context.Background(), plan, t.TempDir(), CollectionExecution{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatal("aggregate duration budget reset", err)
	}
}

func TestCollectionCancellationAndTimeout(t *testing.T) {
	plan := collectionFixture(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, export := range []bool{false, true} {
		var err error
		if export {
			_, err = New(Config{}).ExecuteCollection(ctx, plan, dir, CollectionExecution{})
		} else {
			_, err = New(Config{}).PreflightCollection(ctx, plan, dir, CollectionExecution{})
		}
		if CodeOf(err) != CodeCancelled || !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
	}
	if _, err := New(Config{InspectTimeout: time.Nanosecond}).PreflightCollection(context.Background(), plan, dir, CollectionExecution{}); CodeOf(err) != CodeTimeout {
		t.Fatal("inspect timeout lost", err)
	}
	if _, err := New(Config{ExportTimeout: time.Nanosecond}).ExecuteCollection(context.Background(), plan, dir, CollectionExecution{}); CodeOf(err) != CodeTimeout {
		t.Fatal("export timeout lost", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("canceled operation wrote files")
	}
	// Cancellation after publication keeps the first clip discoverable and
	// stops the collection, rather than giving the next clip a new deadline.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	e := New(Config{FFmpegThreads: 1})
	e.publish = func(ctx context.Context, temporary, output string) (os.FileInfo, error) {
		info, err := fsdurable.Publish(ctx, temporary, output)
		if err == nil {
			cancel()
		}
		return info, err
	}
	partial, err := e.ExecuteCollection(ctx, plan, dir, CollectionExecution{})
	if CodeOf(err) != CodeCancelled || len(partial.Outputs) != 1 {
		t.Fatalf("mid-collection cancellation %+v %v", partial, err)
	}
	decodeCollectionMedia(t, partial.Outputs[0].Path)
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("cancellation leaked workspace")
	}
}

func TestCollectionDetectsSourceChangeAndLateCollision(t *testing.T) {
	for _, mode := range []string{"source", "collision"} {
		t.Run(mode, func(t *testing.T) {
			plan := collectionFixture(t)
			dir := t.TempDir()
			e := New(Config{FFmpegThreads: 1})
			calls := 0
			e.publish = func(ctx context.Context, temporary, output string) (os.FileInfo, error) {
				info, err := fsdurable.Publish(ctx, temporary, output)
				calls++
				if err == nil && calls == 1 {
					if mode == "source" {
						err = os.Chtimes(plan.Input, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
					} else {
						err = os.WriteFile(filepath.Join(dir, "vertical.final.mp4"), []byte("concurrent output"), 0600)
					}
				}
				return info, err
			}
			partial, err := e.ExecuteCollection(context.Background(), plan, dir, CollectionExecution{})
			want := CodeSourceChanged
			if mode == "collision" {
				want = CodeOutputExists
			}
			if CodeOf(err) != want || len(partial.Outputs) != 1 {
				t.Fatalf("late %s %+v %v", mode, partial, err)
			}
			decodeCollectionMedia(t, partial.Outputs[0].Path)
			if mode == "collision" {
				blob, _ := os.ReadFile(filepath.Join(dir, "vertical.final.mp4"))
				if string(blob) != "concurrent output" {
					t.Fatal("late collision overwrote output")
				}
			}
		})
	}
}

func decodeCollectionMedia(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if blob, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("independent decode: %v %s", err, blob)
	}
}
