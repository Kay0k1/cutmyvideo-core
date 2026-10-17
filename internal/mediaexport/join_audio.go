package mediaexport

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func CopyAudio(ctx context.Context, c Config, inputs []Input, selection Selection, start, end int64, video, out string, request edit.ExportRequest, limit int64, runtime JoinedRuntime) error {
	if CopyAudioPacketTrimEligible(selection, request) {
		available, err := runtime.SupportsAudioPacketDrop(ctx)
		if err != nil {
			return err
		}
		if available {
			return CopyAudioDirect(ctx, c, inputs, selection, start, end, video, out, request, limit, runtime)
		}
	}
	return CopyAudioStaged(ctx, c, inputs, selection, start, end, video, out, request, limit, runtime)
}

func CopyAudioDirect(ctx context.Context, c Config, inputs []Input, selection Selection, start, end int64, video, out string, request edit.ExportRequest, limit int64, runtime JoinedRuntime) error {
	input := inputs[selection.AudioInput]
	protocols := "file"
	if input.Remote {
		protocols = "http,tcp"
	}
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-copyts", "-protocol_whitelist", "file", "-format_whitelist", InputFormats}
	args = append(args, InputBounds(c.FFmpegThreads)...)
	args = append(args, "-i", video, "-protocol_whitelist", protocols, "-format_whitelist", InputFormats)
	args = append(args, InputBounds(c.FFmpegThreads)...)
	seek := start - input.OffsetMS
	args = append(args, "-itsoffset", Seconds(-seek), "-seek_timestamp", "1", "-ss", Seconds(seek), "-i", input.Path,
		"-map", "0:v:0", "-map", fmt.Sprintf("1:%d", selection.Audio.Index), "-c", "copy",
		"-bsf:a", "noise=amount=0:drop=lt(pts\\,0)*not(eq(pts\\,nopts))", "-t", Seconds(end-start),
		"-avoid_negative_ts", "disabled", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10))
	args = append(args, mediainfo.AudioMetadataArgs(selection.Audio)...)
	args = append(args, out)
	if err := runtime.Run(ctx, args, 0, nil); err != nil {
		return runtime.CopyDiagnostic(err, request.Format)
	}
	size, err := runtime.StatSize(out)
	if err != nil {
		return err
	}
	if size >= limit {
		return ErrOutputLimit
	}
	return nil
}

func CopyAudioStaged(ctx context.Context, c Config, inputs []Input, selection Selection, start, end int64, video, out string, request edit.ExportRequest, limit int64, runtime JoinedRuntime) error {
	audioInput := inputs[selection.AudioInput]
	protocols := "file"
	if audioInput.Remote {
		protocols = "http,tcp"
	}
	audioPath := runtime.JoinPath(runtime.BaseDir(video), "audio.mka")
	defer runtime.Remove(audioPath)
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", protocols, "-format_whitelist", InputFormats}
	args = append(args, InputBounds(c.FFmpegThreads)...)
	args = append(args, "-seek_timestamp", "1", "-ss", Seconds(start-audioInput.OffsetMS), "-i", audioInput.Path, "-ss", "0", "-t", Seconds(end-start), "-map", fmt.Sprintf("0:%d", selection.Audio.Index), "-c:a", "copy", "-avoid_negative_ts", "disabled", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10), audioPath)
	if err := runtime.Run(ctx, args, 0, nil); err != nil {
		return runtime.CopyDiagnostic(err, request.Format)
	}
	audioSize, err := runtime.StatSize(audioPath)
	if err != nil {
		return err
	}
	if audioSize >= limit {
		return ErrOutputLimit
	}
	args = []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-copyts"}
	for _, path := range []string{video, audioPath} {
		args = append(args, "-protocol_whitelist", "file", "-format_whitelist", InputFormats)
		args = append(args, InputBounds(c.FFmpegThreads)...)
		args = append(args, "-i", path)
	}
	args = append(args, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-t", Seconds(end-start), "-avoid_negative_ts", "disabled", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(limit, 10))
	args = append(args, mediainfo.AudioMetadataArgs(selection.Audio)...)
	args = append(args, out)
	if err := runtime.Run(ctx, args, 0, nil); err != nil {
		return runtime.CopyDiagnostic(err, request.Format)
	}
	size, err := runtime.StatSize(out)
	if err != nil {
		return err
	}
	if size >= limit {
		return ErrOutputLimit
	}
	return nil
}
