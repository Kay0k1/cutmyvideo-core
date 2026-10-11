package app

import (
	"context"
	"fmt"
	"math"
)

// Keep probing to the fields that determine stream selection and rendering.
// In particular, do not request arbitrary tags or the full display matrix.
const mediaProbeEntries = "format=duration,start_time:stream=index,codec_type,codec_name,start_time,width,height,sample_rate,channels,time_base,extradata_hash,sample_aspect_ratio,pix_fmt,color_primaries,color_transfer,color_space,color_range:stream_disposition=default,attached_pic:stream_side_data=rotation"

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
