package app

import (
	"context"
	"fmt"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
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

func probeInputSelection(ctx context.Context, c Config, inputs []mediaInput) (mediaSelection, error) {
	return probeInputSelectionOptions(ctx, c, inputs, ExportRequest{})
}

func probeInputSelectionOptions(ctx context.Context, c Config, inputs []mediaInput, request ExportRequest) (mediaSelection, error) {
	if len(inputs) < 1 || len(inputs) > 2 {
		return mediaSelection{}, errUnsupportedStream
	}
	first, err := probeStreams(ctx, c, inputs[0].Path, inputs[0].Remote)
	if err != nil {
		return mediaSelection{}, err
	}
	selection := mediaSelection{Video: first.selectedVideo(), Audio: first.selectedAudio(), mp3DurationMode: first.ownedMP3DurationMode()}
	if len(inputs) == 2 {
		// Existing separate progressive pairs have video in input zero and
		// audio in input one. Do not silently substitute audio from the video.
		second, err := probeStreams(ctx, c, inputs[1].Path, inputs[1].Remote)
		if err != nil {
			return mediaSelection{}, err
		}
		selection.Audio, selection.AudioInput = second.selectedAudio(), 1
	}
	if request.NoAudio {
		selection.Audio = nil
	} else if request.AudioTrack != nil {
		selectedInput := first
		if len(inputs) == 2 {
			return mediaSelection{}, &sourceProblem{"audio_track_unavailable", "Selecting an audio track requires a local source with inspectable tracks"}
		}
		selection.Audio = nil
		for i := range selectedInput.Streams {
			stream := &selectedInput.Streams[i]
			if stream.Index == *request.AudioTrack && stream.CodecType == "audio" {
				selection.Audio = stream
				break
			}
		}
		if selection.Audio == nil {
			return mediaSelection{}, &sourceProblem{"audio_track_unavailable", "The selected audio stream index is not available; inspect the source and choose an audio index"}
		}
	}
	return selection, nil
}

func (s mediaSelection) mapArgs(format string) []string {
	var args []string
	if format != "mp3" && s.Video != nil {
		args = append(args, "-map", fmt.Sprintf("%d:%d", s.VideoInput, s.Video.Index))
	}
	if s.Audio != nil {
		args = append(args, "-map", fmt.Sprintf("%d:%d", s.AudioInput, s.Audio.Index))
	}
	return args
}

func (p *probeInfo) validateStreams() error {
	return mediaInfoError((*mediainfo.Info)(p).ValidateStreams())
}

func (p *probeInfo) tracks() []Track {
	return (*mediainfo.Info)(p).Tracks()
}

func subtitleTextSupported(codec string) bool {
	return mediainfo.SubtitleTextSupported(codec)
}

func trackManifest(p probeInfo, selection mediaSelection, request ExportRequest) ([]TrackSelection, error) {
	manifest, err := mediainfo.TrackManifest(mediainfo.Info(p), selection.Video, selection.Audio, edit.ExportRequest(request))
	return manifest, mediaInfoError(err)
}

// InspectExportLocal reports source tracks and their fate for selected options.
func InspectExportLocal(ctx context.Context, c Config, path string, request ExportRequest) ([]TrackSelection, error) {
	if err := validateCropMode(request); err != nil {
		return nil, &sourceProblem{"crop_invalid", err.Error()}
	}
	p, err := probeStreams(ctx, c, path, false)
	if err != nil {
		return nil, err
	}
	if request.Crop != nil {
		if _, _, err := videoOutputGeometryWithCrop(p.selectedVideo(), request.Quality, false, request.Crop); err != nil {
			return nil, err
		}
	}
	return SelectExportTracks(p.tracks(), request)
}

func audioMetadataArgs(audio *probeStream) []string {
	return mediainfo.AudioMetadataArgs(audio)
}

// SelectExportTracks computes the same deterministic selection from inspected
// tracks without running another tool. All tracks remain visible in the result.
func SelectExportTracks(tracks []Track, request ExportRequest) ([]TrackSelection, error) {
	manifest, err := mediainfo.SelectExportTracks(tracks, edit.ExportRequest(request))
	return manifest, mediaInfoError(err)
}
