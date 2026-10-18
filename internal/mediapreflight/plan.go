package mediapreflight

import (
	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type CapabilityGroup struct {
	Listing, Code string
	Required      []string
}

func Prepare(c edit.Limits, path string, request edit.ExportRequest, p mediainfo.Info, duration int64) (Result, []CapabilityGroup, error) {
	return PrepareWithReplacement(c, path, request, p, duration, nil)
}

// PrepareWithReplacement receives only already-authorized inspected stream
// facts. It never resolves a source ID or opens a caller-supplied path.
func PrepareWithReplacement(c edit.Limits, path string, request edit.ExportRequest, p mediainfo.Info, duration int64, replacement *mediainfo.Stream) (Result, []CapabilityGroup, error) {
	source := Source{DurationMS: duration}
	if video := p.SelectedVideo(); video != nil {
		source.Width, source.Height = video.Width, video.Height
		source.DisplayWidth, source.DisplayHeight, _ = mediainfo.DisplayGeometry(video)
	}
	// Validate normalizes labels and defaults. Keep that mutation private to
	// this report rather than changing the caller's reusable request.
	request.Ranges = append([]edit.Range(nil), request.Ranges...)
	if request.AudioTrack != nil {
		value := *request.AudioTrack
		request.AudioTrack = &value
	}
	if request.Crop != nil {
		value := *request.Crop
		request.Crop = &value
	}
	request.AudioNormalize = edit.CloneAudioNormalize(request.AudioNormalize)
	request.Canvas = edit.CloneCanvas(request.Canvas)
	request.AudioReplace = edit.CloneAudioReplace(request.AudioReplace)
	request.Speed = edit.CloneSpeed(request.Speed)
	if err := edit.ValidateExportRequest(&request, c, edit.Source{DurationMS: duration, HasLocalMedia: path != ""}); err != nil {
		return Result{}, nil, &mediainfo.Problem{Code: "invalid_argument", Message: err.Error()}
	}
	selection := mediaexport.Selection{Video: p.SelectedVideo(), Audio: p.SelectedAudio()}
	if request.AudioReplace != nil {
		if replacement == nil {
			return Result{}, nil, &mediainfo.Problem{Code: "audio_track_unavailable", Message: "replacement recording must be inspected before preflight"}
		}
		selection.Audio = replacement
	}
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
			return Result{}, nil, &mediainfo.Problem{Code: "audio_track_unavailable", Message: "The selected audio stream index is not available; inspect the source and choose an audio index"}
		}
	}
	tracks, err := mediainfo.TrackManifest(p, selection.Video, selection.Audio, request)
	if err != nil {
		return Result{}, nil, err
	}
	if request.AudioNormalize != nil && selection.Audio == nil {
		return Result{}, nil, mediaexport.ErrNoAudio
	}
	video, err := ValidateSelection(selection, request)
	if err != nil {
		return Result{}, nil, err
	}
	result := RequestResult(source, request)
	result.TrackManifest, result.Video = tracks, video
	result.Checks = append(result.Checks, Check{Name: "source", Status: "passed"}, Check{Name: "tracks", Status: "passed"})
	if video != nil {
		result.Checks = append(result.Checks, Check{Name: "video_geometry", Status: "passed"})
	}
	return result, RequiredCapabilities(selection, request, tracks, video), nil
}

func ValidateSelection(selection mediaexport.Selection, request edit.ExportRequest) (*Video, error) {
	if edit.IsAudioOnly(request.Format) {
		if selection.Audio == nil {
			return nil, mediaexport.ErrNoAudio
		}
		if edit.IsLosslessAudio(request.Format) {
			if _, err := mediaexport.LosslessAudioProfile(selection.Audio, request.Format); err != nil {
				return nil, err
			}
		}
		if request.Format == "m4a" && selection.Audio.CodecName != "aac" {
			return nil, &mediainfo.Problem{Code: "copy_audio_unsupported", Message: "M4A copy requires an AAC audio stream"}
		}
		return nil, nil
	}
	if selection.Video == nil {
		return nil, mediainfo.ErrUnsupported
	}
	if request.CutMode == "copy" {
		if request.Quality != "best" {
			_, height, err := mediainfo.DisplayGeometry(selection.Video)
			if err != nil {
				return nil, err
			}
			limit := 1080
			if request.Quality == "720p" {
				limit = 720
			}
			if height > limit {
				return nil, &mediainfo.Problem{Code: "copy_quality_unsupported", Message: "Copy mode cannot reduce this video's resolution. Choose original quality or accurate mode"}
			}
		}
		return &Video{Width: selection.Video.Width, Height: selection.Video.Height}, nil
	}
	_, hdr, err := mediainfo.RenderFilterWithCanvas(selection.Video, request.Quality, request.Crop, request.Canvas)
	if err != nil {
		return nil, err
	}
	if _, err := mediainfo.EncoderTimeBase(selection.Video); err != nil {
		return nil, err
	}
	width, height, err := mediainfo.RenderGeometry(selection.Video, request.Quality, request.Crop, request.Canvas)
	return &Video{Width: width, Height: height, HDRToSDR: hdr}, err
}

func Finish(result *Result) {
	if result.Request.CutMode == "copy" {
		AddDeferred(result, "copy_boundaries", "copy_boundaries_deferred", "Copy boundaries are determined from source packets and nearby keyframes during export")
		AddDeferred(result, "copy_container", "copy_container_unverified", "The configured muxer verifies whether the selected codecs can be copied into the requested container during export")
	}
	AddDeferred(result, "media_decode", "media_decode_deferred", "Source payload decoding and completed output verification occur during export")
	AddDeferred(result, "output_size", "output_size_deferred", "The completed output must satisfy its size limit during export")
	AddDeferred(result, "storage", "storage_admission_deferred", "Available storage and execution admission are checked when an export is started")
	AddDeferred(result, "publication", "publication_deferred", "Output paths, filesystem capabilities and exclusive publication are checked during execution")
}

func RequiredCapabilities(selection mediaexport.Selection, request edit.ExportRequest, tracks []edit.TrackSelection, video *Video) []CapabilityGroup {
	var encoders, filters, demuxers, bsfs []string
	muxer := request.Format
	if muxer == "m4a" {
		muxer = "ipod"
	}
	if muxer == "mkv" {
		muxer = "matroska"
	}
	muxers := []string{muxer}
	joined := request.OutputMode == "joined"
	if request.CutMode != "copy" {
		if request.Canvas != nil {
			filters = append(filters, "pad")
		}
		if request.AudioNormalize != nil {
			filters = append(filters, "loudnorm")
		}
		if edit.RequiresRenderPipeline(request) && selection.Audio != nil {
			filters = append(filters, "atrim")
		}
		if request.AudioReplace != nil {
			filters = append(filters, "apad")
		}
		if request.Speed != nil {
			filters = append(filters, "atempo", "apad")
			if !edit.IsAudioOnly(request.Format) {
				filters = append(filters, "setpts")
			}
		}
		if request.TargetSizeBytes > 0 {
			muxers = append(muxers, "null")
		}
		if edit.IsLosslessAudio(request.Format) {
			muxers = append(muxers, "wav")
			demuxers = append(demuxers, "wav")
			filters = append(filters, "apad")
			if request.AudioReplace != nil {
				filters = append(filters, "adelay")
			}
			if request.Format == "wav" {
				encoders = append(encoders, "pcm_s24le")
			} else {
				encoders = append(encoders, "flac", "pcm_s24le")
			}
		} else if request.Format == "mp3" {
			encoders = append(encoders, "libmp3lame")
		} else {
			encoders = append(encoders, "libx264")
			filters = append(filters, "scale", "setsar")
			if request.Crop != nil {
				filters = append(filters, "crop")
			}
			if selection.Audio != nil {
				encoders = append(encoders, "aac")
			}
			if video != nil && video.HDRToSDR {
				filters = append(filters, "zscale", "format", "setparams", "tonemap")
				bsfs = append(bsfs, "h264_metadata")
			}
			if joined || edit.RequiresRenderPipeline(request) {
				filters = append(filters, "select")
			}
		}
		if (joined || edit.RequiresRenderPipeline(request)) && selection.Audio != nil {
			filters = append(filters, "aselect", "aresample")
		}
	}
	if joined || edit.RequiresRenderPipeline(request) {
		demuxers = append(demuxers, "concat")
		if request.CutMode == "copy" {
			if request.Format == "m4a" {
				demuxers = append(demuxers, "mov")
			} else if request.Format == "mkv" {
				demuxers = append(demuxers, "matroska")
			} else {
				demuxers = append(demuxers, "mov")
				// Joined MP4 copy always stages selected audio as Matroska;
				// the optional direct packet path is limited to MKV.
				if selection.Audio != nil {
					muxers = append(muxers, "matroska")
					demuxers = append(demuxers, "matroska")
				}
			}
		}
	}
	for _, track := range tracks {
		if !track.Included || track.Type != "subtitle" {
			continue
		}
		encoders = append(encoders, "subrip")
		muxers = append(muxers, "srt")
		demuxers = append(demuxers, "srt")
		if request.Format == "mp4" {
			encoders = append(encoders, "mov_text")
			demuxers = append(demuxers, "mov")
		} else {
			demuxers = append(demuxers, "matroska")
		}
		break
	}
	return []CapabilityGroup{
		{"encoders", "encoder_unavailable", encoders},
		{"muxers", "muxer_unavailable", muxers},
		{"filters", "filter_unavailable", filters},
		{"demuxers", "demuxer_unavailable", demuxers},
		{"bsfs", "bitstream_filter_unavailable", bsfs},
	}
}
