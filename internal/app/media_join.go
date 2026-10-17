package app

import (
	"context"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
)

func ExportJoinedLocal(ctx context.Context, c Config, path, out string, ranges []Range, request ExportRequest) ([]ArtifactSegment, error) {
	if localExportSession(ctx, path, false) == nil {
		var session *LocalExportSession
		ctx, session = BeginLocalExport(ctx, path)
		session.SetWorkspace(filepath.Dir(out))
		session.SetRanges(ranges)
		defer session.Close()
	}
	return exportJoinedInputsProgress(ctx, c, []mediaInput{{Path: path}}, ranges, request, out, nil)
}

func exportJoinedInputsProgress(ctx context.Context, c Config, inputs []mediaInput, ranges []Range, request ExportRequest, out string, report func(int64)) ([]ArtifactSegment, error) {
	segments := make([][]mediaInput, len(ranges))
	for i := range segments {
		segments[i] = inputs
	}
	return exportJoinedSegmentInputsProgress(ctx, c, segments, ranges, request, out, report)
}

func exportJoinedSegmentInputsProgress(ctx context.Context, c Config, inputs [][]mediaInput, ranges []Range, request ExportRequest, out string, report func(int64)) ([]ArtifactSegment, error) {
	segments, err := mediaexport.ExportJoined(ctx, mediaExportConfig(c), inputs, ranges, edit.ExportRequest(request), out, report, newJoinedMediaRuntime(c))
	return segments, mediaExportError(err)
}

func joinAccurate(ctx context.Context, c Config, inputs [][]mediaInput, ranges []Range, request ExportRequest, out, dir string, limit int64, selection mediaSelection, report func(int64)) ([]ArtifactSegment, error) {
	return joinAccurateMode(ctx, c, inputs, ranges, request, out, dir, limit, selection, report, true)
}

func joinAccurateMode(ctx context.Context, c Config, inputs [][]mediaInput, ranges []Range, request ExportRequest, out, dir string, limit int64, selection mediaSelection, report func(int64), finalOutput bool) ([]ArtifactSegment, error) {
	segments, err := mediaexport.JoinAccurate(ctx, mediaExportConfig(c), inputs, ranges, edit.ExportRequest(request), out, dir, limit, mediaExportSelection(selection), report, finalOutput, newJoinedMediaRuntime(c))
	return segments, mediaExportError(err)
}

func verifyJoined(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64) (probeInfo, error) {
	info, err := mediaexport.VerifyJoined(ctx, path, edit.ExportRequest(request), expected, limit, newJoinedMediaRuntime(c))
	return probeInfo(info), mediaExportError(err)
}

func joinCopyAudio(ctx context.Context, c Config, inputs []mediaInput, selection mediaSelection, start, end int64, video, out string, request ExportRequest, limit int64) error {
	return mediaExportError(mediaexport.CopyAudio(ctx, mediaExportConfig(c), inputs, mediaExportSelection(selection), start, end, video, out, edit.ExportRequest(request), limit, newJoinedMediaRuntime(c)))
}

func copyAudioPacketTrimEligible(selection mediaSelection, request ExportRequest) bool {
	return mediaexport.CopyAudioPacketTrimEligible(mediaExportSelection(selection), edit.ExportRequest(request))
}

func joinCopyAudioDirect(ctx context.Context, c Config, inputs []mediaInput, selection mediaSelection, start, end int64, video, out string, request ExportRequest, limit int64) error {
	return mediaExportError(mediaexport.CopyAudioDirect(ctx, mediaExportConfig(c), inputs, mediaExportSelection(selection), start, end, video, out, edit.ExportRequest(request), limit, newJoinedMediaRuntime(c)))
}

func joinCopyAudioStaged(ctx context.Context, c Config, inputs []mediaInput, selection mediaSelection, start, end int64, video, out string, request ExportRequest, limit int64) error {
	return mediaExportError(mediaexport.CopyAudioStaged(ctx, mediaExportConfig(c), inputs, mediaExportSelection(selection), start, end, video, out, edit.ExportRequest(request), limit, newJoinedMediaRuntime(c)))
}
