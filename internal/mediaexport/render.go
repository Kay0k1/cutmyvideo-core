package mediaexport

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// RenderRuntime is the optional effect boundary for explicit transformations.
// Legacy Runtime implementations remain valid for requests without them.
type RenderRuntime interface {
	Runtime
	Replacement(context.Context, edit.AudioReplace) (Input, *mediainfo.Stream, error)
	RunWithDiagnostics(context.Context, []string) ([]byte, error)
	NewWorkspace(string, string) (string, error)
	RemoveAll(string) error
	JoinPath(...string) string
	BaseDir(string) string
	ResolveLocalPath(string) (string, error)
	WritePlan(string, []byte) error
	CheckSource(context.Context, Input) error
}

type renderProvider interface{ RenderRuntime() RenderRuntime }

func transformedExport(ctx context.Context, c Config, inputs [][]Input, ranges []edit.Range, request edit.ExportRequest, out string, report func(int64), final bool, runtime Runtime) ([]edit.Segment, error) {
	if err := edit.ValidateRenderOptions(request); err != nil {
		return nil, &mediainfo.Problem{Code: "invalid_argument", Message: err.Error()}
	}
	provider, ok := runtime.(renderProvider)
	if !ok {
		return nil, errors.New("media backend does not support the requested rendering options")
	}
	rt := provider.RenderRuntime()
	if rt == nil {
		return nil, errors.New("media backend does not support the requested rendering options")
	}
	if len(ranges) == 0 || len(ranges) > 128 || len(inputs) != len(ranges) {
		return nil, errors.New("rendered export requires 1 through 128 input intervals")
	}
	dir, err := rt.NewWorkspace(rt.BaseDir(out), ".render-")
	if err != nil {
		return nil, err
	}
	defer rt.RemoveAll(dir)
	var selection Selection
	for i, row := range inputs {
		if len(row) < 1 || len(row) > 2 || len(row) != len(inputs[0]) {
			return nil, mediainfo.ErrUnsupported
		}
		for _, input := range row {
			if err := rt.CheckSource(ctx, input); err != nil {
				return nil, err
			}
		}
		s, err := rt.SelectInputs(ctx, row, request)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			selection = s
		} else if !SameJoinedStream(selection.Video, s.Video) || !SameJoinedStream(selection.Audio, s.Audio) {
			return nil, &mediainfo.Problem{Code: "join_incompatible", Message: "joined intervals require compatible source streams"}
		}
	}
	if request.Format != "mp3" && selection.Video == nil {
		return nil, mediainfo.ErrUnsupported
	}
	if request.AudioReplace != nil {
		external, audio, err := rt.Replacement(ctx, *request.AudioReplace)
		if err != nil {
			return nil, err
		}
		selection.Audio = audio
		selection.AudioInput = len(inputs[0])
		for i := range inputs {
			row := append([]Input(nil), inputs[i]...)
			inputs[i] = append(row, external)
		}
	}
	if request.AudioNormalize != nil && selection.Audio == nil || request.Format == "mp3" && selection.Audio == nil {
		return nil, ErrNoAudio
	}
	threads := c.FFmpegThreads
	if threads < 1 || threads > 32 {
		threads = 2
	}
	base := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-xerror", "-max_alloc", "268435456", "-filter_threads", strconv.Itoa(threads), "-filter_complex_threads", strconv.Itoa(threads), "-copyts"}
	var elapsed int64
	var segments []edit.Segment
	for i, r := range ranges {
		if r.StartMS < 0 || r.EndMS <= r.StartMS || r.EndMS-r.StartMS > 24*3600000 || r.EndMS-r.StartMS > 24*3600000-elapsed {
			return nil, errors.New("rendered intervals exceed the supported finite timeline")
		}
		d := r.EndMS - r.StartMS
		segments = append(segments, edit.Segment{Index: i, Label: r.Label, SourceStartMS: r.StartMS, SourceEndMS: r.EndMS, OutputStartMS: elapsed, OutputEndMS: elapsed + d})
		elapsed += d
	}
	for col := range inputs[0] {
		var plan strings.Builder
		plan.WriteString("ffconcat version 1.0\n")
		protocols := "file"
		for i, r := range ranges {
			input := inputs[i][col]
			path := input.Path
			child := "file"
			if input.Remote {
				child = "http,tcp"
				protocols = "file,http,tcp"
			} else {
				path, err = rt.ResolveLocalPath(path)
				if err != nil {
					return nil, err
				}
			}
			if strings.ContainsAny(path, "\r\n") {
				return nil, &mediainfo.Problem{Code: "join_incompatible", Message: "rendered source paths cannot contain line breaks"}
			}
			localStart, localEnd := r.StartMS-input.OffsetMS, r.EndMS-input.OffsetMS
			if localStart < 0 && !(request.AudioReplace != nil && col == selection.AudioInput) {
				return nil, mediainfo.ErrUnsupported
			}
			plan.WriteString(FFConcatFile(path))
			fmt.Fprintf(&plan, "option protocol_whitelist %s\noption format_whitelist %s\ninpoint %s\noutpoint %s\nduration %s\n", child, InputFormats, Seconds(localStart), Seconds(localEnd), Seconds(r.EndMS-r.StartMS))
		}
		path := rt.JoinPath(dir, fmt.Sprintf("input-%d.ffconcat", col))
		if err := rt.WritePlan(path, []byte(plan.String())); err != nil {
			return nil, err
		}
		base = append(base, "-protocol_whitelist", protocols, "-format_whitelist", InputFormats+",concat")
		decoderThreads := threads
		if request.Format != "mp3" && col == selection.VideoInput && threads <= 2 {
			decoderThreads = 1
		}
		base = append(base, InputBounds(decoderThreads)...)
		base = append(base, "-segment_time_metadata", "1", "-safe", "0", "-i", path)
	}
	audioFilter := "aselect=concatdec_select,aresample=async=1:first_pts=0"
	if request.AudioReplace != nil {
		audioFilter += ",apad"
	}
	audioFilter += ",atrim=duration=" + Seconds(elapsed)
	if request.AudioNormalize != nil {
		measure := append([]string(nil), base...)
		measure[2] = "info"
		measure = append(measure, "-map", fmt.Sprintf("%d:%d", selection.AudioInput, selection.Audio.Index), "-vn", "-sn", "-af", audioFilter+","+LoudnessFilter(*request.AudioNormalize, nil), "-t", Seconds(elapsed), "-f", "null", "-")
		data, err := rt.RunWithDiagnostics(ctx, measure)
		if err != nil {
			return nil, err
		}
		measurement, err := ParseLoudnessReport(data)
		if err != nil {
			return nil, err
		}
		if measurement.IntegratedLUFS == nil && measurement.TruePeakDBTP != nil {
			return nil, errors.New("signal is too short or too quiet for gated loudness normalization")
		}
		audioFilter += "," + LoudnessFilter(*request.AudioNormalize, &measurement)
	}
	args := append([]string(nil), base...)
	if report != nil {
		args = append(args, "-nostats", "-progress", "pipe:1", "-stats_period", "0.5")
	}
	args = append(args, selection.MapArgs(request.Format)...)
	args = append(args, "-t", Seconds(elapsed))
	hdr := false
	if request.Format != "mp3" {
		filter, isHDR, err := mediainfo.RenderFilterWithCanvas(selection.Video, request.Quality, request.Crop, request.Canvas)
		if err != nil {
			return nil, err
		}
		hdr = isHDR
		clock, err := mediainfo.EncoderTimeBase(selection.Video)
		if err != nil {
			return nil, err
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
		args = append(args, "-af", audioFilter)
		if request.Format == "mp3" {
			args = append(args, "-c:a", "libmp3lame", "-b:a", "192k")
		} else {
			rate := "192k"
			if selection.Audio.Channels == 1 {
				rate = "128k"
			}
			args = append(args, "-c:a", "aac", "-b:a", rate)
		}
		// loudnorm's dynamic true-peak processing upsamples internally. Choose a
		// portable explicit encoder rate instead of inheriting its 192 kHz rate.
		if request.AudioNormalize != nil {
			args = append(args, "-ar", "48000")
		}
	}
	request.Ranges = ranges
	limit := JoinedOutputBudget(c, request)
	args = append(args, mediainfo.AudioMetadataArgs(selection.Audio)...)
	args = append(args, "-threads", strconv.Itoa(threads), "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10))
	if request.Format == "mp4" && final {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	if err := rt.Run(ctx, args, elapsed, report); err != nil {
		if size, e := rt.StatSize(out); e == nil && size >= limit {
			return nil, ErrOutputLimit
		}
		return nil, rt.RenderedFailure(err, hdr)
	}
	p, duration, err := rt.Probe(ctx, out, false)
	if err != nil {
		return nil, err
	}
	size, err := rt.StatSize(out)
	if err != nil {
		return nil, err
	}
	if size >= limit {
		return nil, ErrOutputLimit
	}
	if math.Abs(float64(duration-elapsed)) > 350 {
		return nil, errors.New("rendered result duration does not match the requested timeline")
	}
	if request.Format != "mp3" {
		if err := mediainfo.ValidateRenderedVideoWithCanvas(p, selection.Video, request.Quality, hdr, request.Crop, request.Canvas); err != nil {
			return nil, err
		}
	}
	if selection.Audio != nil && p.SelectedAudio() == nil {
		return nil, ErrNoAudio
	}
	for _, row := range inputs {
		for _, input := range row {
			if err := rt.CheckSource(ctx, input); err != nil {
				return nil, err
			}
		}
	}
	return segments, nil
}
