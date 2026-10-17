package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
)

// Text subtitles are extracted once in their source clock, clipped to the
// actual export bounds and placed on the new output clock. Subtitle packets
// cannot simply be copied: a cue can begin before a cut and span its boundary.
type subtitleCue = subtitles.Cue

func parseSRT(data []byte) ([]subtitleCue, error) { return subtitles.ParseSourceSRT(data) }
func subtitleTimestamp(ms int64) string {
	return subtitles.Timestamp(ms)
}
func rebaseSubtitles(cues []subtitleCue, segments []ArtifactSegment) ([]byte, error) {
	return rebaseIndexedSubtitles(newSubtitleIndex(cues), segments)
}

func rebaseIndexedSubtitles(index *subtitleIndex, segments []ArtifactSegment) ([]byte, error) {
	var out strings.Builder
	number := 0
	for _, segment := range segments {
		for _, cue := range index.Intersecting(segment.SourceStartMS, segment.SourceEndMS) {
			start, end := max(cue.StartMS, segment.SourceStartMS), min(cue.EndMS, segment.SourceEndMS)
			if end <= start {
				continue
			}
			if out.Len()+len(cue.Text)+100 > 8<<20 {
				return nil, &sourceProblem{"subtitle_unsupported", "Preserved text subtitles exceed the 8 MiB per-track limit; choose omit or shorter intervals"}
			}
			number++
			fmt.Fprintf(&out, "%d\n%s --> %s\n%s\n\n", number, subtitleTimestamp(segment.OutputStartMS+start-segment.SourceStartMS), subtitleTimestamp(segment.OutputStartMS+end-segment.SourceStartMS), cue.Text)
		}
	}
	return []byte(out.String()), nil
}
func exportInputsProgress(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64)) (int64, int64, error) {
	if request.SubtitleMode != "preserve" {
		return exportInputsCoreProgress(ctx, c, inputs, r, request, out, report)
	}
	if len(inputs) != 1 || inputs[0].Remote {
		return 0, 0, &sourceProblem{"subtitle_unavailable", "Preserving subtitles requires a local source with inspectable text tracks"}
	}
	p, e := probeStreams(ctx, c, inputs[0].Path, false)
	if e != nil {
		return 0, 0, e
	}
	selection, e := probeInputSelectionOptions(ctx, c, inputs, request)
	if e != nil {
		return 0, 0, e
	}
	if _, e = trackManifest(p, selection, request); e != nil {
		return 0, 0, e
	}
	var count int
	for _, s := range p.Streams {
		if s.CodecType == "subtitle" {
			count++
		}
	}
	if count == 0 {
		return exportInputsCoreProgress(ctx, c, inputs, r, request, out, report)
	}
	dir, e := os.MkdirTemp(filepath.Dir(out), ".subtitles-")
	if e != nil {
		return 0, 0, e
	}
	defer os.RemoveAll(dir)
	media := filepath.Join(dir, "media."+request.Format)
	start, end, e := exportInputsCoreProgressMode(ctx, c, inputs, r, request, media, report, false)
	if e != nil {
		return 0, 0, e
	}
	segments := []ArtifactSegment{{SourceStartMS: start, SourceEndMS: end, OutputEndMS: end - start}}
	e = muxTextSubtitles(ctx, c, inputs[0].Path, p, media, out, segments, request, outputBudget(c, r, request))
	return start, end, e
}

func extractTextSubtitles(ctx context.Context, c Config, source string, stream probeStream) ([]byte, error) {
	session := localExportSession(ctx, source, false)
	if session == nil {
		return extractTextSubtitlesUncached(ctx, c, source, stream)
	}
	data, err := session.Session.ExtractText(ctx, stream.Index, func() ([]byte, error) {
		return extractTextSubtitlesUncached(ctx, c, source, stream)
	})
	return data, mediaInfoError(err)
}

func extractTextSubtitlesUncached(ctx context.Context, c Config, source string, stream probeStream) ([]byte, error) {
	extract := []string{"-v", "error", "-nostdin", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	extract = append(extract, mediaInputBounds(c)...)
	extract = append(extract, "-copyts", "-i", source, "-map", "0:"+strconv.Itoa(stream.Index), "-c:s", "srt", "-f", "srt", "-")
	data, err := runCommand(ctx, c.FFmpeg, extract...)
	if errors.Is(err, errProcessResponseTooLarge) {
		return nil, &sourceProblem{"subtitle_unsupported", "Source text subtitles exceed the 8 MiB per-track limit; choose omit"}
	}
	return data, err
}

func muxTextSubtitles(ctx context.Context, c Config, source string, p probeInfo, media, out string, segments []ArtifactSegment, request ExportRequest, limit int64) error {
	dir, e := os.MkdirTemp(filepath.Dir(out), ".cues-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-copyts", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-i", media)
	var streams []probeStream
	for _, stream := range p.Streams {
		if stream.CodecType != "subtitle" {
			continue
		}
		if !subtitleTextSupported(stream.CodecName) {
			return &sourceProblem{"subtitle_unsupported", "This subtitle format is not supported; choose omit"}
		}
		index, e := sourceSubtitleIndex(ctx, c, source, stream)
		if e != nil {
			return e
		}
		data, e := rebaseIndexedSubtitles(index, segments)
		if e != nil {
			return e
		}
		// An empty SRT has no stream for FFmpeg to map. Use a zero-duration,
		// invisible cue outside the output interval to retain an empty track.
		if len(data) == 0 {
			data = []byte("1\n99:00:00,000 --> 99:00:00,001\n\u200b\n\n")
		}
		path := filepath.Join(dir, fmt.Sprintf("track-%d.srt", stream.Index))
		if e = os.WriteFile(path, data, 0600); e != nil {
			return e
		}
		args = append(args, "-protocol_whitelist", "file", "-format_whitelist", "srt", "-i", path)
		streams = append(streams, stream)
	}
	args = append(args, "-map", "0")
	for i, s := range streams {
		args = append(args, "-map", fmt.Sprintf("%d:0", i+1))
		if s.Tags.Language != "" {
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "language="+s.Tags.Language)
		}
		if s.Tags.Title != "" {
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "title="+s.Tags.Title)
		}
		args = append(args, fmt.Sprintf("-disposition:s:%d", i), strconv.Itoa(s.Disposition.Default))
	}
	codec := "mov_text"
	if request.Format == "mkv" {
		codec = "srt"
	}
	duration := segments[len(segments)-1].OutputEndMS
	args = append(args, "-c", "copy", "-c:s", codec, "-t", seconds(duration), "-map_chapters", "-1", "-avoid_negative_ts", "disabled", "-fs", strconv.FormatInt(limit, 10))
	if request.Format == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	if _, e = runCommand(ctx, c.FFmpeg, args...); e != nil {
		return e
	}
	info, e := os.Stat(out)
	if e != nil {
		return e
	}
	if info.Size() >= limit {
		return errOutputLimit
	}
	final, finalDuration, e := probe(ctx, c, out, false)
	if e != nil {
		return e
	}
	if final.selectedVideo() == nil {
		return errUnsupportedStream
	}
	if math.Abs(float64(finalDuration-duration)) > 350 {
		return fmt.Errorf("subtitle mux changed output duration")
	}
	var count int
	for _, s := range final.Streams {
		if s.CodecType == "subtitle" {
			count++
		}
	}
	if count != len(streams) {
		return fmt.Errorf("result lost preserved subtitle tracks")
	}
	return nil
}
