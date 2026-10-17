package mediaexport

import (
	"context"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

type unavailableRenderRuntime struct{ *exportRuntimeFixture }

func (unavailableRenderRuntime) RenderRuntime() RenderRuntime { return nil }

func TestRenderedExportRefusesUnavailableOptionalRuntime(t *testing.T) {
	request := exportFixtureRequest()
	request.Canvas = &edit.Canvas{Width: 192, Height: 192, Fit: "contain", Background: "#000000"}
	base := newExportRuntimeFixture()
	for _, runtime := range []Runtime{base, unavailableRenderRuntime{base}} {
		_, _, err := Export(context.Background(), exportFixtureConfig(), []Input{{Path: "source.mp4"}}, edit.Range{EndMS: 1000}, request, "out.mp4", nil, true, nil, runtime)
		if err == nil || !strings.Contains(err.Error(), "does not support") || len(base.events) != 0 {
			t.Fatal("unavailable renderer reached media effects", err, base.events)
		}
	}
}
