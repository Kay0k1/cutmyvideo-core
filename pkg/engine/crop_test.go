package engine

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAdvancedCropPlanPublishesActualCroppedOutput(t *testing.T) {
	input := fixture(t)
	ctx := context.Background()
	e := New(Config{MaxOutputBytes: 10 << 20, FFmpegThreads: 1})
	source, err := e.Inspect(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{Version: 1, Input: input, OutputMode: "joined", Ranges: []NamedRange{{StartMS: 2000, EndMS: 2500}, {StartMS: 0, EndMS: 500}}, Options: AdvancedOptions{Options: Options{Format: "mp4"}, Crop: &Crop{Width: .5, Height: 1}}}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var restored Plan
	if err = json.Unmarshal(raw, &restored); err != nil || restored.Options.Crop == nil || *restored.Options.Crop != *plan.Options.Crop {
		t.Fatalf("crop plan round trip: %+v %v", restored, err)
	}
	output := filepath.Join(t.TempDir(), "crop.mp4")
	result, err := e.ExecutePlan(ctx, restored, output)
	if err != nil || len(result.Outputs) != 1 {
		t.Fatalf("cropped plan: %+v %v", result, err)
	}
	info, err := e.Inspect(ctx, output)
	if err != nil || info.DisplayWidth != source.DisplayWidth/4*2 || info.DisplayHeight != source.DisplayHeight/2*2 {
		t.Fatalf("crop output geometry: %+v %v, input %+v", info, err, source)
	}
	if raw, err = exec.Command("ffmpeg", "-v", "error", "-i", output, "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("independent crop output decoding: %v %s", err, raw)
	}
	for _, options := range []AdvancedOptions{
		{Options: Options{Format: "mp4", CutMode: "copy"}, Crop: plan.Options.Crop},
		{Options: Options{Format: "mkv", CutMode: "copy"}, Crop: plan.Options.Crop},
		{Options: Options{Format: "mp3"}, Crop: plan.Options.Crop},
		{Options: Options{Format: "mp4"}, Crop: &Crop{Width: .001, Height: 1}},
	} {
		_, err = e.ExportWithOptions(ctx, input, filepath.Join(t.TempDir(), "invalid."+options.Format), Range{EndMS: 500}, options)
		if CodeOf(err) != CodeInvalidArgument {
			t.Fatalf("incompatible or subpixel crop: code=%s %v", CodeOf(err), err)
		}
	}
}
