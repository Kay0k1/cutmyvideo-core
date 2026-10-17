package mediaexport

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func JoinAccurate(ctx context.Context, c Config, inputs [][]Input, ranges []edit.Range, request edit.ExportRequest, out, dir string, limit int64, selection Selection, report func(int64), finalOutput bool, runtime JoinedRuntime) ([]edit.Segment, error) {
	threads := c.FFmpegThreads
	if threads < 1 || threads > 32 {
		threads = 2
	}
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-filter_threads", strconv.Itoa(threads), "-copyts"}
	if report != nil {
		args = append(args, "-nostats", "-progress", "pipe:1", "-stats_period", "0.5")
	}
	for column := range inputs[0] {
		var plan strings.Builder
		plan.WriteString("ffconcat version 1.0\n")
		protocols := "file"
		for i, r := range ranges {
			input := inputs[i][column]
			if r.StartMS < input.OffsetMS {
				return nil, mediainfo.ErrUnsupported
			}
			path := input.Path
			if input.Remote {
				protocols = "file,http,tcp"
			} else {
				absolute, e := runtime.ResolveLocalPath(path)
				if e != nil {
					return nil, e
				}
				path = absolute
			}
			if strings.ContainsAny(path, "\r\n") {
				return nil, &mediainfo.Problem{Code: "join_incompatible", Message: "Joined source paths cannot contain line breaks; rename the file or use separate outputs"}
			}
			plan.WriteString(FFConcatFile(path))
			// The trusted concat demuxer needs file access to read this plan,
			// but each untrusted media entry retains the original media and
			// transport allowlists. It must never inherit concat/file access
			// merely because the parent plan requires them.
			childProtocols := "file"
			if input.Remote {
				childProtocols = "http,tcp"
			}
			fmt.Fprintf(&plan, "option protocol_whitelist %s\noption format_whitelist %s\n", childProtocols, InputFormats)
			fmt.Fprintf(&plan, "inpoint %s\noutpoint %s\nduration %s\n", Seconds(r.StartMS-input.OffsetMS), Seconds(r.EndMS-input.OffsetMS), Seconds(r.EndMS-r.StartMS))
		}
		path := runtime.JoinPath(dir, fmt.Sprintf("input-%d.ffconcat", column))
		if e := runtime.WritePlan(path, []byte(plan.String())); e != nil {
			return nil, e
		}
		args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", InputFormats+",concat")
		inputConfig := c
		// At a one- or two-thread codec budget, one joined-video decoder
		// avoids an extra frame pipeline. Keep the encoder/filter budget and
		// explicitly larger operator budgets unchanged.
		if request.Format != "mp3" && column == selection.VideoInput && threads <= 2 {
			inputConfig.FFmpegThreads = 1
		}
		args = append(args, InputBounds(inputConfig.FFmpegThreads)...)
		args = append(args, "-segment_time_metadata", "1", "-safe", "0", "-i", path)
	}
	args = append(args, selection.MapArgs(request.Format)...)
	var segments []edit.Segment
	var elapsed int64
	for i, r := range ranges {
		duration := r.EndMS - r.StartMS
		segments = append(segments, edit.Segment{Index: i, Label: r.Label, SourceStartMS: r.StartMS, SourceEndMS: r.EndMS, OutputStartMS: elapsed, OutputEndMS: elapsed + duration})
		elapsed += duration
	}
	args = append(args, "-t", Seconds(elapsed))
	if request.Format != "mp3" {
		filter, hdr, e := mediainfo.CropRenderFilter(selection.Video, request.Quality, request.Crop)
		if e != nil {
			return nil, e
		}
		clock, e := mediainfo.EncoderTimeBase(selection.Video)
		if e != nil {
			return nil, e
		}
		rate := VideoBitrate(c, edit.Range{EndMS: elapsed}, request)
		args = append(args, "-vf", "select=concatdec_select,"+filter, "-c:v", "libx264", "-maxrate", strconv.FormatInt(rate, 10), "-bufsize", strconv.FormatInt(rate*2, 10), "-pix_fmt", "yuv420p", "-fps_mode:v", "passthrough", "-enc_time_base:v", clock)
		if CollectionDraft(ctx) {
			args = append(args, "-preset", "ultrafast", "-crf", "28", "-coder", "1")
		} else if c.FFmpegProfile == "compact" {
			args = append(args, "-preset", "veryfast", "-crf", "20")
		} else {
			args = append(args, "-preset", "ultrafast", "-crf", "18", "-coder", "1")
		}
		args = append(args, mediainfo.HDROutputColorArgs(hdr)...)
	}
	if selection.Audio != nil {
		args = append(args, "-af", "aselect=concatdec_select,aresample=async=1:first_pts=0")
		if request.Format == "mp3" {
			args = append(args, "-c:a", "libmp3lame", "-b:a", "192k")
		} else {
			rate := "192k"
			if selection.Audio.Channels == 1 {
				rate = "128k"
			}
			args = append(args, "-c:a", "aac", "-b:a", rate)
		}
	}
	args = append(args, mediainfo.AudioMetadataArgs(selection.Audio)...)
	args = append(args, "-threads", strconv.Itoa(threads), "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10))
	if request.Format == "mp4" && finalOutput {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	e := runtime.Run(ctx, args, elapsed, report)
	if e != nil {
		return nil, e
	}
	final, e := runtime.VerifyAccurate(ctx, out, request, elapsed, limit, selection)
	if e != nil {
		return nil, e
	}
	if selection.Audio != nil && final.SelectedAudio() == nil {
		return nil, ErrNoAudio
	}
	if request.Format != "mp3" {
		_, hdr, e := mediainfo.CropRenderFilter(selection.Video, request.Quality, request.Crop)
		if e != nil {
			return nil, e
		}
		if e = mediainfo.ValidateRenderedVideoWithCrop(final, selection.Video, request.Quality, false, hdr, request.Crop); e != nil {
			return nil, e
		}
	}
	return segments, nil
}
