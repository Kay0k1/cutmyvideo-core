package mediaexport

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// exportAAC selects a preceding AAC packet and reports that actual source
// boundary. It never invokes an audio encoder or falls back to another format.
func exportAAC(ctx context.Context, c Config, inputs []Input, r edit.Range, request edit.ExportRequest, out string, report func(int64), final bool, verified *mediainfo.Info, runtime Runtime) (int64, int64, error) {
	if request.CutMode != "copy" || edit.HasRenderOptions(request) || request.NoAudio || request.SubtitleMode == "preserve" || request.Crop != nil {
		return 0, 0, &mediainfo.Problem{Code: "invalid_argument", Message: "M4A requires AAC copy without rendering options"}
	}
	sel, err := runtime.SelectInputs(ctx, inputs, request)
	if err != nil {
		return 0, 0, err
	}
	if sel.Audio == nil {
		return 0, 0, ErrNoAudio
	}
	if sel.Audio.CodecName != "aac" {
		return 0, 0, &mediainfo.Problem{Code: "copy_audio_unsupported", Message: "M4A copy requires an AAC audio stream; choose WAV, FLAC or MP3 to encode other audio"}
	}
	input := inputs[sel.AudioInput]
	provider, ok := runtime.(renderProvider)
	if !ok {
		return 0, 0, &mediainfo.Problem{Code: "copy_incompatible", Message: "media backend cannot fence AAC packet-copy inputs"}
	}
	fence := provider.RenderRuntime()
	if fence == nil {
		return 0, 0, &mediainfo.Problem{Code: "copy_incompatible", Message: "media backend cannot fence AAC packet-copy inputs"}
	}
	if err := fence.CheckSource(ctx, input); err != nil {
		return 0, 0, err
	}
	packet, err := runtime.NearestKeyframe(ctx, input.Path, r.StartMS-input.OffsetMS, input.Remote, sel.Audio.Index)
	if err != nil {
		return 0, 0, err
	}
	start := packet.PTS + input.OffsetMS
	seek := start
	if packet.HasDTS {
		seek = packet.DTS + input.OffsetMS
	}
	if start < 0 || start > r.StartMS || start >= r.EndMS {
		return 0, 0, &mediainfo.Problem{Code: "copy_incompatible", Message: "AAC packet boundary is unavailable for this interval"}
	}
	protocols := "file"
	if input.Remote {
		protocols = "http,tcp"
	}
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", protocols, "-format_whitelist", InputFormats}
	args = append(args, InputBounds(c.FFmpegThreads)...)
	args = append(args, "-seek_timestamp", "1", "-ss", Seconds(seek-input.OffsetMS), "-i", input.Path, "-ss", "0", "-t", Seconds(r.EndMS-seek), "-map", fmt.Sprintf("0:%d", sel.Audio.Index), "-vn", "-sn", "-c:a", "copy", "-avoid_negative_ts", "make_zero", "-map_metadata", "-1", "-map_chapters", "-1")
	args = append(args, mediainfo.AudioMetadataArgs(sel.Audio)...)
	limit := OutputBudget(c, r, request)
	args = append(args, "-fs", strconv.FormatInt(limit, 10), "-f", "ipod")
	if final {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	if err := runtime.Run(ctx, args, r.EndMS-start, report); err != nil {
		return 0, 0, runtime.CopyDiagnostic(err, "m4a")
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
	if err := ValidateAudioOutput(p, sel.Audio, "m4a"); err != nil {
		return 0, 0, err
	}
	// Container/sample rounding is bounded to two AAC frames (including encoder
	// priming). A graceful byte-limit truncation is never published as success.
	rate, _ := strconv.Atoi(sel.Audio.SampleRate)
	tolerance := int64(100)
	if rate > 0 {
		tolerance = max(5, int64(math.Ceil(2048*1000/float64(rate))))
	}
	if math.Abs(float64(duration-(r.EndMS-start))) > float64(tolerance) {
		return 0, 0, &mediainfo.Problem{Code: "copy_incompatible", Message: fmt.Sprintf("AAC copy duration %d ms does not match selected packet interval %d..%d ms", duration, start, r.EndMS)}
	}
	if verified != nil {
		*verified = p
	}
	if err := fence.CheckSource(ctx, input); err != nil {
		return 0, 0, err
	}
	return start, start + duration, nil
}

func joinAAC(ctx context.Context, c Config, inputs [][]Input, ranges []edit.Range, request edit.ExportRequest, out string, report func(int64), runtime JoinedRuntime) ([]edit.Segment, error) {
	if len(ranges) == 0 || len(ranges) > 128 || len(inputs) != len(ranges) {
		return nil, &mediainfo.Problem{Code: "invalid_argument", Message: "AAC join requires1 through128 input intervals"}
	}
	dir, err := runtime.NewWorkspace(runtime.BaseDir(out), ".aac-join-")
	if err != nil {
		return nil, err
	}
	defer runtime.RemoveAll(dir)
	var plan strings.Builder
	plan.WriteString("ffconcat version 1.0\n")
	var result []edit.Segment
	var elapsed, bytes int64
	var fingerprint *mediainfo.Stream
	limit := JoinedOutputBudget(c, request)
	for i, r := range ranges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("audio-%d.m4a", i)
		path := runtime.JoinPath(dir, name)
		var info mediainfo.Info
		start, end, err := exportAAC(ctx, c, inputs[i], r, request, path, nil, false, &info, runtime)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			fingerprint = info.SelectedAudio()
		} else if !SameJoinedStream(fingerprint, info.SelectedAudio()) {
			return nil, &mediainfo.Problem{Code: "join_incompatible", Message: "AAC joined intervals require matching codec configuration"}
		}
		size, err := runtime.StatSize(path)
		if err != nil {
			return nil, err
		}
		if size > limit-bytes {
			return nil, ErrOutputLimit
		}
		bytes += size
		plan.WriteString(FFConcatFile(name))
		fmt.Fprintf(&plan, "duration %s\n", Seconds(end-start))
		result = append(result, edit.Segment{Index: i, Label: r.Label, SourceStartMS: start, SourceEndMS: end, OutputStartMS: elapsed, OutputEndMS: elapsed + end - start})
		elapsed += end - start
	}
	path := runtime.JoinPath(dir, "audio.ffconcat")
	if err := runtime.WritePlan(path, []byte(plan.String())); err != nil {
		return nil, err
	}
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", InputFormats + ",concat", "-safe", "1", "-i", path, "-map", "0:a:0", "-vn", "-sn", "-c:a", "copy", "-avoid_negative_ts", "make_zero", "-map_metadata", "-1", "-map_chapters", "-1", "-movflags", "+faststart", "-fs", strconv.FormatInt(limit, 10), "-f", "ipod", out}
	if err := runtime.Run(ctx, args, elapsed, report); err != nil {
		return nil, runtime.CopyDiagnostic(err, "m4a")
	}
	p, err := VerifyJoined(ctx, out, request, elapsed, limit, runtime)
	if err != nil {
		return nil, err
	}
	if err := ValidateAudioOutput(p, fingerprint, "m4a"); err != nil {
		return nil, err
	}
	if provider, ok := runtime.(renderProvider); ok {
		for _, row := range inputs {
			for _, input := range row {
				if err := provider.RenderRuntime().CheckSource(ctx, input); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}
