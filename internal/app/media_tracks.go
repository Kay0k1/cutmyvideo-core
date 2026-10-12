package app

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Keep probing to the fields that determine stream selection and rendering.
// In particular, do not request arbitrary tags or the full display matrix.
const mediaProbeEntries = "format=duration,start_time:stream=index,codec_type,codec_name,start_time,width,height,sample_rate,channels,time_base,extradata_hash,sample_aspect_ratio,pix_fmt,color_primaries,color_transfer,color_space,color_range:stream_disposition=default,attached_pic:stream_tags=language,title:stream_side_data=rotation"

type probeStream struct {
	Index             int    `json:"index"`
	CodecType         string `json:"codec_type"`
	CodecName         string `json:"codec_name"`
	SampleRate        string `json:"sample_rate"`
	Channels          int    `json:"channels"`
	TimeBase          string `json:"time_base"`
	ExtradataHash     string `json:"extradata_hash"`
	StartTime         string `json:"start_time"`
	Width             int    `json:"width"`
	Height            int    `json:"height"`
	SampleAspectRatio string `json:"sample_aspect_ratio"`
	PixelFormat       string `json:"pix_fmt"`
	ColorPrimaries    string `json:"color_primaries"`
	ColorTransfer     string `json:"color_transfer"`
	ColorSpace        string `json:"color_space"`
	ColorRange        string `json:"color_range"`
	Disposition       struct {
		Default     int `json:"default"`
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
	Tags struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
	SideDataList []probeSideData `json:"side_data_list"`
}

type probeSideData struct {
	Rotation *float64 `json:"rotation"`
}

func (s *probeStream) rotation() float64 {
	for _, data := range s.SideDataList {
		if data.Rotation != nil {
			return *data.Rotation
		}
	}
	return 0
}

// A cover image is not the playable video. Source indices, rather than the
// order of JSON fields, define the first stream in a container.
func (p *probeInfo) selectedVideo() *probeStream {
	var selected *probeStream
	for i := range p.Streams {
		s := &p.Streams[i]
		if s.CodecType == "video" && s.Disposition.AttachedPic == 0 && (selected == nil || s.Index < selected.Index) {
			selected = s
		}
	}
	return selected
}

// Prefer the first default audio stream, otherwise the first audio stream.
// Stream titles/languages do not silently override this deterministic policy.
func (p *probeInfo) selectedAudio() *probeStream {
	var selected *probeStream
	for i := range p.Streams {
		s := &p.Streams[i]
		if s.CodecType != "audio" {
			continue
		}
		if selected == nil || s.Disposition.Default != 0 && selected.Disposition.Default == 0 || (s.Disposition.Default != 0) == (selected.Disposition.Default != 0) && s.Index < selected.Index {
			selected = s
		}
	}
	return selected
}

type mediaSelection struct {
	Video, Audio           *probeStream
	VideoInput, AudioInput int
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
	selection := mediaSelection{Video: first.selectedVideo(), Audio: first.selectedAudio()}
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
	if len(p.Streams) == 0 || len(p.Streams) > maxMediaStreams {
		return fmt.Errorf("source contains no supported streams")
	}
	indices := make(map[int]struct{}, len(p.Streams))
	for _, stream := range p.Streams {
		if _, duplicate := indices[stream.Index]; duplicate || stream.Index < 0 {
			return fmt.Errorf("source contains invalid stream indices")
		}
		indices[stream.Index] = struct{}{}
		if stream.CodecType == "video" && (stream.Width <= 0 || stream.Height <= 0 || stream.Width > maxMediaPixels/stream.Height) {
			return fmt.Errorf("source video dimensions exceed the decoder limit")
		}
		for _, field := range []string{stream.CodecType, stream.CodecName, stream.SampleRate, stream.TimeBase, stream.StartTime, stream.SampleAspectRatio, stream.PixelFormat, stream.ColorPrimaries, stream.ColorTransfer, stream.ColorSpace, stream.ColorRange} {
			if len(field) > 64 {
				return fmt.Errorf("source stream metadata exceeds the field limit")
			}
		}
		if len(stream.Tags.Language) > 128 || len(stream.Tags.Title) > 512 {
			return fmt.Errorf("source track tags exceed the field limit")
		}
		if len(stream.ExtradataHash) > 80 || len(stream.SideDataList) > 32 {
			return fmt.Errorf("source stream metadata exceeds the field limit")
		}
		for _, data := range stream.SideDataList {
			if data.Rotation != nil && (math.IsNaN(*data.Rotation) || math.IsInf(*data.Rotation, 0)) {
				return fmt.Errorf("source has invalid display rotation")
			}
		}
	}
	return nil
}

// Track identifies an absolute source stream index, rather than an audio ordinal.
type Track struct {
	Index           int    `json:"index"`
	Type            string `json:"type"`
	Codec           string `json:"codec"`
	Language        string `json:"language,omitempty"`
	Title           string `json:"title,omitempty"`
	Channels        int    `json:"channels,omitempty"`
	SampleRate      string `json:"sample_rate,omitempty"`
	Default         bool   `json:"default"`
	AttachedPicture bool   `json:"attached_picture,omitempty"`
}

// TrackSelection explains every included or excluded source stream.
type TrackSelection struct {
	Track
	Included bool   `json:"included"`
	Reason   string `json:"reason"`
}

func (p *probeInfo) tracks() []Track {
	tracks := make([]Track, 0, len(p.Streams))
	for _, s := range p.Streams {
		tracks = append(tracks, Track{Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Language: strings.TrimSpace(s.Tags.Language), Title: strings.TrimSpace(s.Tags.Title), Channels: s.Channels, SampleRate: s.SampleRate, Default: s.Disposition.Default != 0, AttachedPicture: s.Disposition.AttachedPic != 0})
	}
	return tracks
}

func subtitleTextSupported(codec string) bool {
	return codec == "subrip" || codec == "mov_text" || codec == "webvtt" || codec == "text"
}

func trackManifest(p probeInfo, selection mediaSelection, request ExportRequest) ([]TrackSelection, error) {
	manifest := make([]TrackSelection, 0, len(p.Streams))
	for _, track := range p.tracks() {
		item := TrackSelection{Track: track, Reason: "unsupported_stream"}
		if track.Type == "video" {
			item.Reason = "video_not_selected"
		}
		if track.Type == "audio" {
			item.Reason = "audio_not_selected"
		}
		if track.Type == "video" && selection.Video != nil && track.Index == selection.Video.Index && request.Format != "mp3" {
			item.Included = true
			item.Reason = "selected_video"
		}
		if track.Type == "audio" && selection.Audio != nil && track.Index == selection.Audio.Index {
			item.Included = true
			item.Reason = "selected_audio"
		}
		if track.Type == "audio" && request.NoAudio {
			item.Reason = "audio_disabled"
		}
		if track.Type == "subtitle" {
			item.Reason = "subtitles_omitted"
			if request.SubtitleMode == "preserve" {
				if !subtitleTextSupported(track.Codec) {
					return nil, &sourceProblem{"subtitle_unsupported", "Subtitle stream " + strconv.Itoa(track.Index) + " uses " + track.Codec + "; preserve supports plain text subrip, mov_text, webvtt and text. Choose omit for this source"}
				}
				item.Included = true
				item.Reason = "subtitles_preserved"
			}
		}
		manifest = append(manifest, item)
	}
	return manifest, nil
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
	if audio == nil {
		return nil
	}
	args := []string{"-disposition:a:0", "default"}
	if audio.Tags.Language != "" {
		args = append(args, "-metadata:s:a:0", "language="+audio.Tags.Language)
	}
	if audio.Tags.Title != "" {
		args = append(args, "-metadata:s:a:0", "title="+audio.Tags.Title)
	}
	return args
}

// SelectExportTracks computes the same deterministic selection from inspected
// tracks without running another tool. All tracks remain visible in the result.
func SelectExportTracks(tracks []Track, request ExportRequest) ([]TrackSelection, error) {
	var p probeInfo
	for _, track := range tracks {
		stream := probeStream{Index: track.Index, CodecType: track.Type, CodecName: track.Codec, Channels: track.Channels, SampleRate: track.SampleRate}
		stream.Tags.Language, stream.Tags.Title = track.Language, track.Title
		if track.Default {
			stream.Disposition.Default = 1
		}
		if track.AttachedPicture {
			stream.Disposition.AttachedPic = 1
		}
		p.Streams = append(p.Streams, stream)
	}
	selection := mediaSelection{Video: p.selectedVideo(), Audio: p.selectedAudio()}
	if request.NoAudio {
		selection.Audio = nil
	} else if request.AudioTrack != nil {
		selection.Audio = nil
		for i := range p.Streams {
			stream := &p.Streams[i]
			if stream.CodecType == "audio" && stream.Index == *request.AudioTrack {
				selection.Audio = stream
				break
			}
		}
		if selection.Audio == nil {
			return nil, &sourceProblem{"audio_track_unavailable", "The selected audio stream index is not available; inspect the source and choose an audio index"}
		}
	}
	return trackManifest(p, selection, request)
}
