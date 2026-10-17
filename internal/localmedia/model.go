package localmedia

import (
	"github.com/Kay0k1/cutmyvideo-core/internal/coremodel"
	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// Package localmedia executes local or already guarded media operations without
// importing the HTTP server, persistence, queue or provider implementation.
type Config = coremodel.Config
type Source = coremodel.Source
type ExportRequest = coremodel.ExportRequest
type Range = edit.Range
type Track = edit.Track
type TrackSelection = edit.TrackSelection
type ArtifactSegment = edit.Segment
type Crop = edit.Crop
type probeInfo mediainfo.Info
type mediaInput = mediaexport.Input
type copyKeyframe = mediaexport.Keyframe

const mediaFormats = mediaexport.InputFormats

var errProcessResponseTooLarge = mediaerror.ErrProcessResponseTooLarge

func mediaInputBounds(c Config) []string { return mediaexport.InputBounds(c.FFmpegThreads) }
func seconds(ms int64) string            { return mediaexport.Seconds(ms) }
func outputBudget(c Config, r Range, request ExportRequest) int64 {
	return mediaexport.OutputBudget(mediaExportConfig(c), r, edit.ExportRequest(request))
}
func mediaInfoError(err error) error {
	if err == mediainfo.ErrUnsupported {
		return mediaerror.ErrUnsupportedStream
	}
	if problem, ok := err.(*mediainfo.Problem); ok {
		return &mediaerror.Problem{Code: problem.Code, Message: problem.Message}
	}
	return err
}
func videoDisplayGeometry(stream *probeStream) (int, int, error) {
	w, h, e := mediainfo.DisplayGeometry(stream)
	return w, h, mediaInfoError(e)
}
func videoOutputGeometryWithCrop(stream *probeStream, quality string, preview bool, crop *Crop) (int, int, error) {
	w, h, e := mediainfo.OutputGeometryWithCrop(stream, quality, preview, crop)
	return w, h, mediaInfoError(e)
}
func validateCropMode(request ExportRequest) error {
	return edit.ValidateCropMode(edit.ExportRequest(request))
}
