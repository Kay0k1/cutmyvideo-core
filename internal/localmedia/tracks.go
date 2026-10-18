package localmedia

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

const mediaProbeEntries = mediainfo.ProbeEntries

type probeStream = mediainfo.Stream

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
	if len(inputs) < 1 || len(inputs) > 2 {
		return mediaSelection{}, mediaerror.ErrUnsupportedStream
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
			return mediaSelection{}, &mediaerror.Problem{Code: "audio_track_unavailable", Message: "Selecting an audio track requires a local source with inspectable tracks"}
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
			return mediaSelection{}, &mediaerror.Problem{Code: "audio_track_unavailable", Message: "The selected audio stream index is not available; inspect the source and choose an audio index"}
		}
	}
	return selection, nil
}

func (s mediaSelection) mapArgs(format string) []string {
	return mediaExportSelection(s).MapArgs(format)
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
	if err := edit.ValidateRenderOptions(edit.ExportRequest(request)); err != nil {
		return nil, &mediaerror.Problem{Code: "invalid_argument", Message: err.Error()}
	}
	ctx, release, err := PrepareAudioReplacement(ctx, request)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := validateCropMode(request); err != nil {
		return nil, &mediaerror.Problem{Code: "crop_invalid", Message: err.Error()}
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
	if request.Canvas != nil {
		if _, _, err := mediainfo.RenderGeometry(p.selectedVideo(), request.Quality, request.Crop, request.Canvas); err != nil {
			return nil, mediaInfoError(err)
		}
	}
	if request.AudioReplace != nil {
		if _, _, err := (mediaExportRuntime{config: c}).Replacement(ctx, *request.AudioReplace); err != nil {
			return nil, err
		}
	} else if request.AudioNormalize != nil {
		selection, err := probeInputSelectionOptions(ctx, c, []mediaInput{{Path: path}}, request)
		if err != nil {
			return nil, err
		}
		if selection.Audio == nil {
			return nil, mediaerror.ErrNoAudio
		}
	}
	return SelectExportTracks(p.tracks(), request)
}

// SelectExportTracks computes the same deterministic selection from inspected
// tracks without running another tool. All tracks remain visible in the result.
func SelectExportTracks(tracks []Track, request ExportRequest) ([]TrackSelection, error) {
	manifest, err := mediainfo.SelectExportTracks(tracks, edit.ExportRequest(request))
	return manifest, mediaInfoError(err)
}

func ProbeInputSelectionOptions(ctx context.Context, c Config, inputs []mediaInput, request ExportRequest) (mediaexport.Selection, error) {
	s, e := probeInputSelectionOptions(ctx, c, inputs, request)
	return mediaExportSelection(s), e
}
