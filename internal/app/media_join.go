package app

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExportJoinedLocal joins ranges from one local source in caller order.
func ExportJoinedLocal(ctx context.Context, c Config, path, out string, ranges []Range, request ExportRequest) ([]ArtifactSegment, error) {
	return exportJoinedInputsProgress(ctx, c, []mediaInput{{Path: path}}, ranges, request, out, nil)
}
func exportJoinedInputsProgress(ctx context.Context, c Config, inputs []mediaInput, ranges []Range, request ExportRequest, out string, report func(int64)) ([]ArtifactSegment, error) {
	segments := make([][]mediaInput, len(ranges))
	for i := range segments {
		segments[i] = inputs
	}
	return exportJoinedSegmentInputsProgress(ctx, c, segments, ranges, request, out, report)
}

func sameJoinedStream(a, b *probeStream) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Index == b.Index && a.CodecName == b.CodecName && a.TimeBase == b.TimeBase && a.ExtradataHash == b.ExtradataHash && a.Width == b.Width && a.Height == b.Height && a.SampleRate == b.SampleRate && a.Channels == b.Channels && a.SampleAspectRatio == b.SampleAspectRatio && a.rotation() == b.rotation()
}

func exportJoinedSegmentInputsProgress(ctx context.Context, c Config, inputs [][]mediaInput, ranges []Range, request ExportRequest, out string, report func(int64)) ([]ArtifactSegment, error) {
	if len(ranges) == 0 || len(inputs) != len(ranges) {
		return nil, fmt.Errorf("joined export needs inputs for every range")
	}
	dir, e := os.MkdirTemp(filepath.Dir(out), ".joined-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	selections := make([]mediaSelection, len(ranges))
	for i, r := range ranges {
		if r.EndMS <= r.StartMS {
			return nil, fmt.Errorf("range %d must end after its start", i+1)
		}
		selections[i], e = probeInputSelectionOptions(ctx, c, inputs[i], request)
		if e != nil {
			return nil, e
		}
		if request.CutMode == "copy" {
			if err := validateCopyContainer(selections[i], request.Format); err != nil {
				return nil, err
			}
		}
		if request.Format != "mp3" && selections[i].Video == nil {
			return nil, errUnsupportedStream
		}
		if request.Format == "mp3" && selections[i].Audio == nil {
			return nil, errNoAudio
		}
		if len(inputs[i]) != len(inputs[0]) || !sameJoinedStream(selections[0].Video, selections[i].Video) || !sameJoinedStream(selections[0].Audio, selections[i].Audio) {
			return nil, &sourceProblem{"join_incompatible", "Joined intervals must use compatible streams from the same source; choose separate outputs"}
		}
	}
	request.Ranges = ranges
	limit := joinedOutputBudget(c, request)
	media := out
	if request.SubtitleMode == "preserve" {
		if len(inputs[0]) != 1 || inputs[0][0].Remote {
			return nil, &sourceProblem{"subtitle_unavailable", "Preserving subtitles requires a local source"}
		}
		p, err := probeStreams(ctx, c, inputs[0][0].Path, false)
		if err != nil {
			return nil, err
		}
		if _, err = trackManifest(p, selections[0], request); err != nil {
			return nil, err
		}
		media = filepath.Join(dir, "media."+request.Format)
	}
	var segments []ArtifactSegment
	if request.CutMode == "copy" {
		segments, e = joinCopy(ctx, c, inputs, ranges, request, media, dir, limit, report)
	} else {
		segments, e = joinAccurate(ctx, c, inputs, ranges, request, media, dir, limit, selections[0], report)
	}
	if e != nil {
		return nil, e
	}
	if request.SubtitleMode == "preserve" {
		p, e := probeStreams(ctx, c, inputs[0][0].Path, false)
		if e != nil {
			return nil, e
		}
		if _, e = trackManifest(p, selections[0], request); e != nil {
			return nil, e
		}
		e = muxTextSubtitles(ctx, c, inputs[0][0].Path, p, media, out, segments, request, limit)
		if e != nil {
			return nil, e
		}
	}
	return segments, nil
}

func ffconcatFile(path string) string {
	return "file '" + strings.ReplaceAll(path, "'", "'\\''") + "'\n"
}
func joinCopy(ctx context.Context, c Config, inputs [][]mediaInput, ranges []Range, request ExportRequest, out, dir string, limit int64, report func(int64)) ([]ArtifactSegment, error) {
	var manifest strings.Builder
	manifest.WriteString("ffconcat version 1.0\n")
	var result []ArtifactSegment
	var elapsed int64
	var fingerprint probeInfo
	var aggregate int64
	for i, r := range ranges {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		name := fmt.Sprintf("segment-%d.%s", i, request.Format)
		path := filepath.Join(dir, name)
		progress := func(ms int64) {
			if report != nil {
				report(elapsed + ms)
			}
		}
		videoRequest := request
		if !request.NoAudio {
			videoRequest.NoAudio = true
			videoRequest.AudioTrack = nil
		}
		videoPath := filepath.Join(dir, fmt.Sprintf("video-%d.%s", i, request.Format))
		start, end, e := exportInputsCoreProgress(ctx, c, inputs[i], r, videoRequest, videoPath, progress)
		if e == nil {
			selection, selectErr := probeInputSelectionOptions(ctx, c, inputs[i], request)
			if selectErr != nil {
				e = selectErr
			} else if selection.Audio != nil {
				e = joinCopyAudio(ctx, c, inputs[i], selection, start, end, videoPath, path, request, min(limit, outputBudget(c, r, request)))
			} else {
				e = os.Rename(videoPath, path)
			}
		}
		_ = os.Remove(videoPath)
		if e != nil {
			return nil, e
		}
		stat, e := os.Stat(path)
		if e != nil {
			return nil, e
		}
		aggregate += stat.Size()
		if aggregate >= limit {
			return nil, errOutputLimit
		}
		p, _, e := probe(ctx, c, path, false)
		if e != nil {
			return nil, e
		}
		if i == 0 {
			fingerprint = p
		} else if !sameJoinedStream(fingerprint.selectedVideo(), p.selectedVideo()) || !sameJoinedStream(fingerprint.selectedAudio(), p.selectedAudio()) {
			return nil, &sourceProblem{"join_incompatible", "Copied intervals have incompatible stream parameters; choose separate outputs or accurate mode"}
		}
		duration := end - start
		manifest.WriteString(ffconcatFile(name))
		fmt.Fprintf(&manifest, "duration %s\n", seconds(duration))
		result = append(result, ArtifactSegment{Index: i, Label: r.Label, SourceStartMS: start, SourceEndMS: end, OutputStartMS: elapsed, OutputEndMS: elapsed + duration})
		elapsed += duration
	}
	path := filepath.Join(dir, "copy.ffconcat")
	if e := os.WriteFile(path, []byte(manifest.String()), 0600); e != nil {
		return nil, e
	}
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats + ",concat", "-safe", "1", "-auto_convert", "0", "-copyts"}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-i", path, "-map", "0", "-c", "copy", "-map_metadata", "-1", "-map_chapters", "-1", "-avoid_negative_ts", "disabled", "-fs", strconv.FormatInt(limit, 10))
	args = append(args, audioMetadataArgs(fingerprint.selectedAudio())...)
	if request.Format == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	if _, e := runCommand(ctx, c.FFmpeg, args...); e != nil {
		return nil, e
	}
	if e := verifyJoined(ctx, c, out, request, elapsed, limit); e != nil {
		return nil, e
	}
	if report != nil {
		report(elapsed)
	}
	return result, nil
}

// The concat demuxer opens one interval at a time. Decoder preroll is discarded
// using its frame metadata, and the source frame cadence survives one encode.
// This avoids opening one decoder per range or encoding a lossy intermediate.
func joinAccurate(ctx context.Context, c Config, inputs [][]mediaInput, ranges []Range, request ExportRequest, out, dir string, limit int64, selection mediaSelection, report func(int64)) ([]ArtifactSegment, error) {
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
				return nil, errUnsupportedStream
			}
			path := input.Path
			if input.Remote {
				protocols = "file,http,tcp"
			} else {
				absolute, e := filepath.Abs(path)
				if e != nil {
					return nil, e
				}
				path = filepath.ToSlash(absolute)
			}
			if strings.ContainsAny(path, "\r\n") {
				return nil, &sourceProblem{"join_incompatible", "Joined source paths cannot contain line breaks; rename the file or use separate outputs"}
			}
			plan.WriteString(ffconcatFile(path))
			// The trusted concat demuxer needs file access to read this plan,
			// but each untrusted media entry retains the original media and
			// transport allowlists. It must never inherit concat/file access
			// merely because the parent plan requires them.
			childProtocols := "file"
			if input.Remote {
				childProtocols = "http,tcp"
			}
			fmt.Fprintf(&plan, "option protocol_whitelist %s\noption format_whitelist %s\n", childProtocols, mediaFormats)
			fmt.Fprintf(&plan, "inpoint %s\noutpoint %s\nduration %s\n", seconds(r.StartMS-input.OffsetMS), seconds(r.EndMS-input.OffsetMS), seconds(r.EndMS-r.StartMS))
		}
		path := filepath.Join(dir, fmt.Sprintf("input-%d.ffconcat", column))
		if e := os.WriteFile(path, []byte(plan.String()), 0600); e != nil {
			return nil, e
		}
		args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats+",concat")
		args = append(args, mediaInputBounds(c)...)
		args = append(args, "-segment_time_metadata", "1", "-safe", "0", "-i", path)
	}
	args = append(args, selection.mapArgs(request.Format)...)
	var segments []ArtifactSegment
	var elapsed int64
	for i, r := range ranges {
		duration := r.EndMS - r.StartMS
		segments = append(segments, ArtifactSegment{Index: i, Label: r.Label, SourceStartMS: r.StartMS, SourceEndMS: r.EndMS, OutputStartMS: elapsed, OutputEndMS: elapsed + duration})
		elapsed += duration
	}
	args = append(args, "-t", seconds(elapsed))
	if request.Format != "mp3" {
		filter, hdr, e := videoRenderFilter(selection.Video, request.Quality, false)
		if e != nil {
			return nil, e
		}
		clock, e := videoEncoderTimeBase(selection.Video)
		if e != nil {
			return nil, e
		}
		rate := exportVideoBitrate(c, Range{EndMS: elapsed}, request)
		args = append(args, "-vf", "select=concatdec_select,"+filter, "-c:v", "libx264", "-maxrate", strconv.FormatInt(rate, 10), "-bufsize", strconv.FormatInt(rate*2, 10), "-pix_fmt", "yuv420p", "-fps_mode:v", "passthrough", "-enc_time_base:v", clock)
		if c.FFmpegProfile == "compact" {
			args = append(args, "-preset", "veryfast", "-crf", "20")
		} else {
			args = append(args, "-preset", "ultrafast", "-crf", "18", "-coder", "1")
		}
		args = append(args, hdrOutputColorArgs(hdr)...)
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
	args = append(args, audioMetadataArgs(selection.Audio)...)
	args = append(args, "-threads", strconv.Itoa(threads), "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10))
	if request.Format == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	var e error
	if report == nil {
		_, e = runCommand(ctx, c.FFmpeg, args...)
	} else {
		e = runCommandOutput(ctx, c.FFmpeg, args, &mediaProgressWriter{totalMS: elapsed, report: report})
	}
	if e != nil {
		return nil, e
	}
	if e = verifyJoined(ctx, c, out, request, elapsed, limit); e != nil {
		return nil, e
	}
	final, _, e := probe(ctx, c, out, false)
	if e != nil {
		return nil, e
	}
	if selection.Audio != nil && final.selectedAudio() == nil {
		return nil, errNoAudio
	}
	if request.Format != "mp3" {
		_, hdr, e := videoRenderFilter(selection.Video, request.Quality, false)
		if e != nil {
			return nil, e
		}
		if e = validateRenderedVideo(final, selection.Video, request.Quality, false, hdr); e != nil {
			return nil, e
		}
	}
	return segments, nil
}

func verifyJoined(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64) error {
	stat, e := os.Stat(path)
	if e != nil {
		return e
	}
	if stat.Size() >= limit {
		return errOutputLimit
	}
	p, duration, e := probe(ctx, c, path, false)
	if e != nil {
		return e
	}
	if request.Format != "mp3" && p.selectedVideo() == nil {
		return errUnsupportedStream
	}
	if request.Format == "mp3" && p.selectedAudio() == nil {
		return errNoAudio
	}
	if math.Abs(float64(duration-expected)) > 350 {
		return fmt.Errorf("joined result duration does not match its selected intervals")
	}
	return nil
}

// B-frame stream-copy limits use DTS. Video can therefore retain presentation
// frames after the requested endpoint while audio stops earlier. Copy audio to
// the measured video endpoint so concat does not create a gap at every boundary.
func joinCopyAudio(ctx context.Context, c Config, inputs []mediaInput, selection mediaSelection, start, end int64, video, out string, request ExportRequest, limit int64) error {
	audioInput := inputs[selection.AudioInput]
	protocols := "file"
	if audioInput.Remote {
		protocols = "http,tcp"
	}
	audioPath := filepath.Join(filepath.Dir(video), "audio.mka")
	defer os.Remove(audioPath)
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-seek_timestamp", "1", "-ss", seconds(start-audioInput.OffsetMS), "-i", audioInput.Path, "-ss", "0", "-t", seconds(end-start), "-map", fmt.Sprintf("0:%d", selection.Audio.Index), "-c:a", "copy", "-avoid_negative_ts", "disabled", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10), audioPath)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return err
	}
	audioStat, err := os.Stat(audioPath)
	if err != nil {
		return err
	}
	if audioStat.Size() >= limit {
		return errOutputLimit
	}
	args = []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-copyts"}
	for _, path := range []string{video, audioPath} {
		args = append(args, "-protocol_whitelist", "file", "-format_whitelist", mediaFormats)
		args = append(args, mediaInputBounds(c)...)
		args = append(args, "-i", path)
	}
	args = append(args, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-t", seconds(end-start), "-avoid_negative_ts", "disabled", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10))
	args = append(args, audioMetadataArgs(selection.Audio)...)
	if request.Format == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return err
	}
	stat, err := os.Stat(out)
	if err != nil {
		return err
	}
	if stat.Size() >= limit {
		return errOutputLimit
	}
	return nil
}
