package app

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

const mediaProbeEntries = mediainfo.ProbeEntries

type probeStream = mediainfo.Stream
type probeSideData = mediainfo.SideData

func (p *probeInfo) selectedVideo() *probeStream {
	return (*mediainfo.Info)(p).SelectedVideo()
}

func (p *probeInfo) selectedAudio() *probeStream {
	return (*mediainfo.Info)(p).SelectedAudio()
}

type mediaSelection struct {
	Video, Audio           *probeStream
	VideoInput, AudioInput int
	mp3DurationMode        int
}

func probeInputSelectionOptions(ctx context.Context, c Config, inputs []mediaInput, request ExportRequest) (mediaSelection, error) {
	s, e := localmedia.ProbeInputSelectionOptions(ctx, c, inputs, request)
	return mediaSelection{Video: s.Video, Audio: s.Audio, VideoInput: s.VideoInput, AudioInput: s.AudioInput, mp3DurationMode: s.MP3DurationMode}, localMediaError(e)
}

func (s mediaSelection) mapArgs(format string) []string {
	return mediaExportSelection(s).MapArgs(format)
}

// InspectExportLocal reports source tracks and their fate for selected options.
func InspectExportLocal(ctx context.Context, c Config, path string, request ExportRequest) ([]TrackSelection, error) {
	tracks, e := localmedia.InspectExportLocal(ctx, c, path, request)
	return tracks, localMediaError(e)
}

// SelectExportTracks computes the same deterministic selection from inspected
// tracks without running another tool. All tracks remain visible in the result.
func SelectExportTracks(tracks []Track, request ExportRequest) ([]TrackSelection, error) {
	manifest, err := mediainfo.SelectExportTracks(tracks, edit.ExportRequest(request))
	return manifest, mediaInfoError(err)
}
