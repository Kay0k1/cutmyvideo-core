// Package mediainfo interprets bounded media metadata and portable media
// decisions without opening files, running tools or depending on the service.
package mediainfo

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

const MaxPixels = 33_554_432 // Includes 7680 x 4320 (8K).
const MaxStreams = 32

// Keep probing to the fields that determine stream selection and rendering.
// In particular, do not request arbitrary tags or the full display matrix.
const ProbeEntries = "format=duration,start_time:stream=index,codec_type,codec_name,start_time,duration,width,height,sample_rate,channels,bits_per_raw_sample,time_base,extradata_hash,sample_aspect_ratio,pix_fmt,color_primaries,color_transfer,color_space,color_range:stream_disposition=default,attached_pic:stream_tags=language,title:stream_side_data=rotation"

type Info struct {
	Format struct {
		Duration  string `json:"duration"`
		StartTime string `json:"start_time"`
	} `json:"format"`
	Streams         []Stream `json:"streams"`
	LibraryVersions []struct {
		Name  string `json:"name"`
		Major int    `json:"major"`
	} `json:"library_versions"`
}

type Stream struct {
	Index             int    `json:"index"`
	CodecType         string `json:"codec_type"`
	CodecName         string `json:"codec_name"`
	SampleRate        string `json:"sample_rate"`
	Channels          int    `json:"channels"`
	BitsPerRawSample  int    `json:"bits_per_raw_sample,string,omitempty"`
	TimeBase          string `json:"time_base"`
	ExtradataHash     string `json:"extradata_hash"`
	StartTime         string `json:"start_time"`
	Duration          string `json:"duration,omitempty"`
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
	SideDataList []SideData `json:"side_data_list"`
}

type SideData struct {
	Rotation *float64 `json:"rotation"`
}

func (s *Stream) Rotation() float64 {
	for _, data := range s.SideDataList {
		if data.Rotation != nil {
			return *data.Rotation
		}
	}
	return 0
}

// A cover image is not the playable video. Source indices, rather than the
// order of JSON fields, define the first stream in a container.
func (p *Info) SelectedVideo() *Stream {
	var selected *Stream
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
func (p *Info) SelectedAudio() *Stream {
	var selected *Stream
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

func (p *Info) ValidateStreams() error {
	if len(p.Streams) == 0 || len(p.Streams) > MaxStreams {
		return fmt.Errorf("source contains no supported streams")
	}
	indices := make(map[int]struct{}, len(p.Streams))
	for _, stream := range p.Streams {
		if _, duplicate := indices[stream.Index]; duplicate || stream.Index < 0 {
			return fmt.Errorf("source contains invalid stream indices")
		}
		indices[stream.Index] = struct{}{}
		if stream.CodecType == "video" && (stream.Width <= 0 || stream.Height <= 0 || stream.Width > MaxPixels/stream.Height) {
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

func (p *Info) DurationMS() (int64, error) {
	duration, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 30*24*3600 {
		return 0, errors.New("source has no supported finite duration")
	}
	return int64(math.Round(duration * 1000)), nil
}

// ErrUnsupported identifies media metadata outside the supported render shape.
var ErrUnsupported = errors.New("unsupported media stream")

// Problem contains a classified decision failure for interface-owned adapters.
type Problem struct {
	Code, Message string
}

func (e *Problem) Error() string { return e.Message }
