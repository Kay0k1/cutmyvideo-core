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

type boundedWorkspaceRunner interface {
	RunBoundedWorkspace(context.Context, []string, int64, func(int64), string, int64) error
}

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
	var passDir string
	var passRunner boundedWorkspaceRunner
	if request.TargetSizeBytes != 0 {
		var ok bool
		passRunner, ok = rt.(boundedWorkspaceRunner)
		if !ok {
			return nil, errors.New("media backend cannot supervise bounded two-pass statistics")
		}
		passDir, err = rt.NewWorkspace(dir, ".passes-")
		if err != nil {
			return nil, err
		}
	}
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
	if !edit.IsAudioOnly(request.Format) && selection.Video == nil {
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
	if request.AudioNormalize != nil && selection.Audio == nil || edit.IsAudioOnly(request.Format) && selection.Audio == nil {
		return nil, ErrNoAudio
	}
	var losslessArgs []string
	if edit.IsLosslessAudio(request.Format) {
		losslessArgs, err = LosslessAudioProfile(selection.Audio, request.Format)
		if err != nil {
			return nil, err
		}
	}
	protectedInputs := inputs
	if edit.IsLosslessAudio(request.Format) {
		inputs, selection, err = prepareLosslessTimeline(ctx, c, rt, dir, inputs, ranges, selection)
		if err != nil {
			return nil, err
		}
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
		from, e := edit.OutputDurationMS(elapsed, request.Speed)
		if e != nil {
			return nil, e
		}
		elapsed += d
		to, e := edit.OutputDurationMS(elapsed, request.Speed)
		if e != nil {
			return nil, e
		}
		segments = append(segments, edit.Segment{Index: i, Label: r.Label, SourceStartMS: r.StartMS, SourceEndMS: r.EndMS, OutputStartMS: from, OutputEndMS: to})
	}
	sourceElapsed := elapsed
	elapsed, err = edit.OutputDurationMS(sourceElapsed, request.Speed)
	if err != nil || elapsed <= 0 {
		return nil, &mediainfo.Problem{Code: "invalid_argument", Message: "speed output timeline must contain at least one millisecond"}
	}
	videoRate, err := targetVideoRate(c, request, elapsed, selection.Audio)
	if err != nil {
		return nil, err
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
		if !edit.IsAudioOnly(request.Format) && col == selection.VideoInput && threads <= 2 {
			decoderThreads = 1
		}
		base = append(base, InputBounds(decoderThreads)...)
		base = append(base, "-segment_time_metadata", "1", "-safe", "0", "-i", path)
	}
	audioFilter := "aselect=concatdec_select,aresample=async=1:first_pts=0"
	if request.AudioReplace != nil {
		audioFilter += ",apad"
	}
	audioFilter += ",atrim=duration=" + Seconds(sourceElapsed)
	if request.Speed != nil {
		rate := float64(request.Speed.Numerator) / float64(request.Speed.Denominator)
		audioFilter += ",atempo=" + strconv.FormatFloat(rate, 'g', 17, 64) + ",apad,atrim=duration=" + Seconds(elapsed)
	}
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
	var videoArgs []string
	if !edit.IsAudioOnly(request.Format) {
		filter, isHDR, err := mediainfo.RenderFilterWithCanvas(selection.Video, request.Quality, request.Crop, request.Canvas)
		if err != nil {
			return nil, err
		}
		hdr = isHDR
		clock, err := mediainfo.EncoderTimeBase(selection.Video)
		if err != nil {
			return nil, err
		}
		if request.Speed != nil {
			filter += fmt.Sprintf(",setpts=PTS*%d/%d", request.Speed.Denominator, request.Speed.Numerator)
		}
		videoArgs = append(videoArgs, "-vf", "select=concatdec_select,"+filter, "-c:v", "libx264", "-maxrate", strconv.FormatInt(videoRate, 10), "-bufsize", strconv.FormatInt(videoRate*2, 10), "-pix_fmt", "yuv420p", "-fps_mode:v", "passthrough", "-enc_time_base:v", clock)
		if CollectionDraft(ctx) {
			videoArgs = append(videoArgs, "-preset", "ultrafast", "-crf", "28", "-coder", "1")
		} else if c.FFmpegProfile == "compact" {
			videoArgs = append(videoArgs, "-preset", "veryfast")
			if request.TargetSizeBytes == 0 {
				videoArgs = append(videoArgs, "-crf", "20")
			}
		} else {
			videoArgs = append(videoArgs, "-preset", "ultrafast", "-coder", "1")
			if request.TargetSizeBytes == 0 {
				videoArgs = append(videoArgs, "-crf", "18")
			}
		}
		if request.TargetSizeBytes != 0 {
			videoArgs = append(videoArgs, "-b:v", strconv.FormatInt(videoRate, 10), "-frames:v", "1000000")
		}
		videoArgs = append(videoArgs, mediainfo.HDROutputColorArgs(hdr)...)
		if request.TargetSizeBytes != 0 {
			pass := append([]string(nil), base...)
			pass = append(pass, "-map", fmt.Sprintf("%d:%d", selection.VideoInput, selection.Video.Index), "-t", Seconds(elapsed))
			pass = append(pass, videoArgs...)
			pass = append(pass, "-an", "-sn", "-threads", strconv.Itoa(threads), "-pass", "1", "-passlogfile", rt.JoinPath(passDir, "video"), "-f", "null", "-")
			if e := passRunner.RunBoundedWorkspace(ctx, pass, elapsed, nil, passDir, TargetPassLogLimit); e != nil {
				return nil, rt.RenderedFailure(e, hdr)
			}
			videoArgs = append(videoArgs, "-pass", "2", "-passlogfile", rt.JoinPath(passDir, "video"))
		}
		args = append(args, videoArgs...)
	}
	if selection.Audio != nil {
		args = append(args, "-af", audioFilter)
		if edit.IsLosslessAudio(request.Format) {
			args = append(args, losslessArgs...)
		} else if request.Format == "mp3" {
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
		if request.AudioNormalize != nil && !edit.IsLosslessAudio(request.Format) {
			args = append(args, "-ar", "48000")
		}
	}
	request.Ranges = ranges
	limit := JoinedOutputBudget(c, request)
	if request.TargetSizeBytes != 0 {
		limit = request.TargetSizeBytes + 1
	}
	args = append(args, mediainfo.AudioMetadataArgs(selection.Audio)...)
	args = append(args, "-threads", strconv.Itoa(threads), "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10))
	if request.Format == "mp4" && final {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	progress := report
	if report != nil && request.Speed != nil {
		inverse := &edit.Speed{Numerator: request.Speed.Denominator, Denominator: request.Speed.Numerator}
		progress = func(ms int64) {
			source, _ := edit.OutputDurationMS(max(0, ms), inverse)
			report(min(sourceElapsed, source))
		}
	}
	var runErr error
	if passRunner != nil {
		runErr = passRunner.RunBoundedWorkspace(ctx, args, elapsed, progress, passDir, TargetPassLogLimit)
	} else {
		runErr = rt.Run(ctx, args, elapsed, progress)
	}
	if err := runErr; err != nil {
		if size, e := rt.StatSize(out); e == nil && size >= limit {
			if request.TargetSizeBytes != 0 {
				return nil, targetFailure("complete encoded output exceeds target_size_bytes")
			}
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
		if request.TargetSizeBytes != 0 {
			return nil, targetFailure("complete encoded output exceeds target_size_bytes")
		}
		return nil, ErrOutputLimit
	}
	if math.Abs(float64(duration-elapsed)) > 350 {
		if request.TargetSizeBytes != 0 {
			return nil, targetFailure("target size cannot contain the complete output timeline")
		}
		return nil, errors.New("rendered result duration does not match the requested timeline")
	}
	if request.TargetSizeBytes != 0 {
		video := p.SelectedVideo()
		if video == nil {
			return nil, targetFailure("target size output has no video stream")
		}
		seconds, e := strconv.ParseFloat(video.Duration, 64)
		if e != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds*1000-float64(elapsed)) > 350 {
			return nil, targetFailure("target size output does not contain the complete video timeline")
		}
	}
	if !edit.IsAudioOnly(request.Format) {
		if err := mediainfo.ValidateRenderedVideoWithCanvas(p, selection.Video, request.Quality, hdr, request.Crop, request.Canvas); err != nil {
			return nil, err
		}
	}
	if selection.Audio != nil && p.SelectedAudio() == nil {
		return nil, ErrNoAudio
	}
	if err := ValidateAudioOutput(p, selection.Audio, request.Format); err != nil {
		return nil, err
	}
	for _, row := range protectedInputs {
		for _, input := range row {
			if err := rt.CheckSource(ctx, input); err != nil {
				return nil, err
			}
		}
	}
	return segments, nil
}
