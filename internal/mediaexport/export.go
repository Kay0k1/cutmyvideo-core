package mediaexport

import (
	"context"
	"errors"
	"math"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// Export coordinates one verified media interval. Runtime owns tools, I/O and
// interface-specific failure classification; this package owns their order.
func Export(ctx context.Context, c Config, inputs []Input, r edit.Range, request edit.ExportRequest, out string, report func(int64), finalOutput bool, verified *mediainfo.Info, runtime Runtime) (int64, int64, error) {
	if edit.HasRenderOptions(request) {
		_, err := transformedExport(ctx, c, [][]Input{inputs}, []edit.Range{r}, request, out, report, finalOutput, runtime)
		if err != nil {
			return 0, 0, err
		}
		if verified != nil {
			p, _, err := runtime.Probe(ctx, out, false)
			if err != nil {
				return 0, 0, err
			}
			*verified = p
		}
		return r.StartMS, r.EndMS, nil
	}
	if err := edit.ValidateCropMode(request); err != nil {
		return 0, 0, &mediainfo.Problem{Code: "crop_invalid", Message: err.Error()}
	}
	selection, err := runtime.SelectInputs(ctx, inputs, request)
	if err != nil {
		return 0, 0, err
	}
	if request.Format == "mp3" && selection.Audio == nil || len(inputs) == 2 && selection.Audio == nil && !request.NoAudio {
		return 0, 0, ErrNoAudio
	}
	if request.Format != "mp3" && selection.Video == nil {
		return 0, 0, mediainfo.ErrUnsupported
	}
	if request.CutMode == "copy" && selection.Video == nil {
		return 0, 0, mediainfo.ErrUnsupported
	}
	filter, hdr := "", false
	encoderTimeBase := ""
	if request.Format != "mp3" && request.CutMode != "copy" {
		filter, hdr, err = mediainfo.CropRenderFilter(selection.Video, request.Quality, request.Crop)
		if err != nil {
			return 0, 0, err
		}
		encoderTimeBase, err = mediainfo.EncoderTimeBase(selection.Video)
		if err != nil {
			return 0, 0, err
		}
	}
	limit := OutputBudget(c, r, request)
	start := r.StartMS
	copySeek, copyShift := int64(0), int64(0)
	copyTrim := false
	if request.CutMode == "copy" {
		if request.Quality != "best" {
			_, displayHeight, err := mediainfo.DisplayGeometry(selection.Video)
			if err != nil {
				return 0, 0, err
			}
			capHeight := 1080
			if request.Quality == "720p" {
				capHeight = 720
			}
			if displayHeight > capHeight {
				return 0, 0, &mediainfo.Problem{Code: "copy_quality_unsupported", Message: "Copy mode cannot reduce this video's resolution. Choose original quality or accurate mode"}
			}
		}
		videoInput := inputs[selection.VideoInput]
		local, err := runtime.NearestKeyframe(ctx, videoInput.Path, start-videoInput.OffsetMS, videoInput.Remote, selection.Video.Index)
		if err != nil {
			return 0, 0, err
		}
		start = local.PTS + videoInput.OffsetMS
		if local.HasDTS {
			copySeek = local.DTS + videoInput.OffsetMS
			copyShift = local.PTS - local.DTS
			copyTrim = true
		} else {
			// First MKV packets can lack DTS until reordering is known. Keep
			// that first keyframe and remove only its presentation-time offset.
			copySeek = videoInput.OffsetMS
			copyShift = local.PTS
		}
	}

	threadCount := c.FFmpegThreads
	if threadCount <= 0 || threadCount > 32 {
		threadCount = 2
	}
	threads := strconv.Itoa(threadCount)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-filter_threads", threads, "-filter_complex_threads", threads}
	if report != nil {
		args = append(args, "-nostats", "-stats_period", "0.5", "-progress", "pipe:1")
	}
	for _, input := range inputs {
		protocols := "file"
		if input.Remote {
			protocols = "http,tcp"
		}
		localStart := start - input.OffsetMS
		if request.CutMode == "copy" {
			localStart = copySeek - input.OffsetMS
		}
		if localStart < 0 && request.CutMode != "copy" {
			return 0, 0, mediainfo.ErrUnsupported
		}
		args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", InputFormats)
		args = append(args, InputBounds(c.FFmpegThreads)...)
		if request.CutMode == "copy" {
			// ffprobe reports packet PTS/DTS in the container's clock. Treat
			// the selected DTS as an absolute timestamp too: adding a nonzero
			// container start would move this keyframe before the output trim.
			args = append(args, "-seek_timestamp", "1")
		}
		args = append(args, "-ss", Seconds(localStart), "-i", input.Path)
	}
	if request.CutMode == "copy" && copyTrim {
		// Sparse container seek indexes may return earlier clusters. Trim at
		// DTS after seeking, preserving the selected B-frame keyframe itself.
		args = append(args, "-ss", "0")
	}
	args = append(args, "-t", Seconds(r.EndMS-start+copyShift))
	args = append(args, selection.MapArgs(request.Format)...)
	if request.Format == "mp3" {
		args = append(args, "-vn", "-c:a", "libmp3lame", "-b:a", "192k")
	} else {
		if request.CutMode == "copy" {
			args = append(args, "-c", "copy", "-avoid_negative_ts", "disabled", "-output_ts_offset", Seconds(-copyShift))
		} else {
			rate := VideoBitrate(c, r, request)
			args = append(args, "-c:v", "libx264", "-maxrate", strconv.FormatInt(rate, 10), "-bufsize", strconv.FormatInt(rate*2, 10))
			if CollectionDraft(ctx) {
				args = append(args, "-preset", "ultrafast", "-crf", "28", "-coder", "1")
			} else if c.FFmpegProfile == "compact" {
				args = append(args, "-preset", "veryfast", "-crf", "20")
			} else {
				// CABAC recovers much of ultrafast's size overhead with little
				// CPU cost on the measured server. CRF 18 retains visual quality
				// while avoiding expensive motion analysis in the default profile.
				args = append(args, "-preset", "ultrafast", "-crf", "18", "-coder", "1")
			}
			audioBitrate := "192k"
			if selection.Audio != nil && selection.Audio.Channels == 1 {
				// 128 kbit/s is ample for mono AAC and avoids the native encoder's
				// 192-kbit/s convergence failure on some valid silent/pulsed input.
				audioBitrate = "128k"
			}
			args = append(args, "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", audioBitrate)
			// Keep the source clock as well as its frame cadence. The default
			// encoder time base can quantize irregular presentation timestamps.
			args = append(args, "-vf", filter, "-fps_mode:v", "passthrough", "-enc_time_base:v", encoderTimeBase)
			args = append(args, mediainfo.HDROutputColorArgs(hdr)...)
		}
		if request.Format == "mp4" && finalOutput {
			args = append(args, "-movflags", "+faststart")
		}
	}
	args = append(args, mediainfo.AudioMetadataArgs(selection.Audio)...)
	args = append(args, "-threads", threads, "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10), out)
	commandErr := runtime.Run(ctx, args, r.EndMS-start, report)
	if err := commandErr; err != nil {
		if size, statErr := runtime.StatSize(out); statErr == nil && size >= limit {
			return 0, 0, ErrOutputLimit
		}
		if request.CutMode == "copy" {
			err = runtime.CopyDiagnostic(err, request.Format)
		}
		return 0, 0, runtime.RenderedFailure(err, hdr)
	}
	size, err := runtime.StatSize(out)
	if err != nil {
		return 0, 0, err
	}
	if size >= limit {
		return 0, 0, ErrOutputLimit
	}
	p, duration, err := runtime.Probe(ctx, out, false)
	if err != nil {
		return 0, 0, err
	}
	if request.Format != "mp3" {
		if p.SelectedVideo() == nil {
			return 0, 0, errors.New("result has no video stream")
		}
		if request.CutMode != "copy" {
			if err := mediainfo.ValidateRenderedVideoWithCrop(p, selection.Video, request.Quality, false, hdr, request.Crop); err != nil {
				return 0, 0, err
			}
		}
	}
	if selection.Audio != nil && p.SelectedAudio() == nil {
		return 0, 0, errors.New("result has no selected audio stream")
	}
	// A duration mismatch also catches FFmpeg's graceful -fs truncation.
	expected := r.EndMS - start
	if request.CutMode == "accurate" && math.Abs(float64(duration-expected)) > 350 {
		return 0, 0, errors.New("result duration does not match the requested range")
	}
	if verified != nil {
		*verified = p
	}
	return start, start + duration, nil
}
