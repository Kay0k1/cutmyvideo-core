package mediaexport

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

// Lossless pieces are decoded sequentially to exact PCM sample boundaries.
// concatdec_select alone selects whole audio frames, which can bleed the final
// frame across a reordered cut. Private PCM pieces bound memory to one decoder
// and avoid that loss without opening128 simultaneous decoders.
func prepareLosslessTimeline(ctx context.Context, c Config, rt RenderRuntime, dir string, inputs [][]Input, ranges []edit.Range, selection Selection) ([][]Input, Selection, error) {
	profile, err := LosslessAudioProfile(selection.Audio, "wav")
	if err != nil {
		return nil, selection, err
	}
	rate, _ := strconv.ParseInt(selection.Audio.SampleRate, 10, 64)
	var requested int64
	for _, r := range ranges {
		requested += r.EndMS - r.StartMS
	}
	workspaceLimit := TransientBudget(c, edit.Range{EndMS: requested}, edit.ExportRequest{Format: "wav"})
	var total int64
	result := make([][]Input, len(ranges))
	for i, r := range ranges {
		if err := ctx.Err(); err != nil {
			return nil, selection, err
		}
		input := inputs[i][selection.AudioInput]
		if err := rt.CheckSource(ctx, input); err != nil {
			return nil, selection, err
		}
		_, sourceDuration, err := rt.Probe(ctx, input.Path, input.Remote)
		if err != nil {
			return nil, selection, err
		}
		start, end := r.StartMS-input.OffsetMS, r.EndMS-input.OffsetMS
		duration := r.EndMS - r.StartMS
		args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456"}
		filter := "aresample=async=1:first_pts=0"
		if end <= 0 || start >= sourceDuration {
			args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("anullsrc=r=%s:cl=%dc", selection.Audio.SampleRate, selection.Audio.Channels))
		} else {
			protocols := "file"
			if input.Remote {
				protocols = "http,tcp"
			}
			args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", InputFormats)
			args = append(args, InputBounds(c.FFmpegThreads)...)
			args = append(args, "-ss", Seconds(max(0, start)), "-i", input.Path, "-map", fmt.Sprintf("0:%d", selection.Audio.Index))
			if start < 0 {
				filter += ",adelay=delays=" + strconv.FormatInt(-start, 10) + ":all=1"
			}
		}
		samples := (duration*rate + 500) / 1000
		filter += ",apad,atrim=end_sample=" + strconv.FormatInt(samples, 10)
		args = append(args, "-vn", "-sn", "-af", filter)
		args = append(args, profile...)
		path := rt.JoinPath(dir, fmt.Sprintf("pcm-%d.wav", i))
		args = append(args, "-threads", strconv.Itoa(max(1, min(32, c.FFmpegThreads))), "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(workspaceLimit-total, 10), path)
		if err := rt.Run(ctx, args, duration, nil); err != nil {
			return nil, selection, err
		}
		size, err := rt.StatSize(path)
		if err != nil {
			return nil, selection, err
		}
		if size >= workspaceLimit-total {
			return nil, selection, ErrOutputLimit
		}
		total += size
		if err := rt.CheckSource(ctx, input); err != nil {
			return nil, selection, err
		}
		result[i] = []Input{{Path: path, OffsetMS: r.StartMS}}
	}
	info, _, err := rt.Probe(ctx, result[0][0].Path, false)
	if err != nil {
		return nil, selection, err
	}
	selection.Video = nil
	selection.VideoInput = 0
	selection.Audio = info.SelectedAudio()
	selection.AudioInput = 0
	return result, selection, nil
}
