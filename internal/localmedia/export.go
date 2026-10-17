package localmedia

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func exportMedia(ctx context.Context, c Config, paths []string, remote bool, r Range, request ExportRequest, out string) (int64, int64, error) {
	inputs := make([]mediaInput, len(paths))
	for i, path := range paths {
		inputs[i] = mediaInput{Path: path, Remote: remote}
	}
	return exportInputs(ctx, c, inputs, r, request, out)
}

func exportInputs(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string) (int64, int64, error) {
	return exportInputsProgress(ctx, c, inputs, r, request, out, nil)
}

func exportInputsCoreProgress(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64)) (int64, int64, error) {
	return exportInputsCoreProgressMode(ctx, c, inputs, r, request, out, report, true)
}

func exportInputsCoreProgressMode(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64), finalOutput bool) (int64, int64, error) {
	return exportInputsCoreVerifiedProgress(ctx, c, inputs, r, request, out, report, finalOutput, nil)
}

func exportInputsCoreVerifiedProgress(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64), finalOutput bool, verified *probeInfo) (int64, int64, error) {
	start, end, err := mediaexport.Export(ctx, mediaExportConfig(c), inputs, r, edit.ExportRequest(request), out, report, finalOutput, (*mediainfo.Info)(verified), mediaExportRuntime{config: c})
	return start, end, mediaExportError(err)
}
func ExportMedia(ctx context.Context, c Config, paths []string, remote bool, r Range, request ExportRequest, out string) (int64, int64, error) {
	return exportMedia(ctx, c, paths, remote, r, request, out)
}
func ExportInputs(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string) (int64, int64, error) {
	return exportInputs(ctx, c, inputs, r, request, out)
}
func ExportInputsCoreVerifiedProgress(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64), finalOutput bool, verified *mediainfo.Info) (int64, int64, error) {
	return exportInputsCoreVerifiedProgress(ctx, c, inputs, r, request, out, report, finalOutput, (*probeInfo)(verified))
}
