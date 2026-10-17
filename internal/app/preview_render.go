package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

func trimPreviewWorkspace(ctx context.Context, dir string) error {
	return trimWindowWorkspace(ctx, dir, "preview.mp4")
}

func trimWindowWorkspace(ctx context.Context, dir, filename string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != filename {
			if err := removeStorageTree(ctx, filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderPreview(ctx context.Context, c Config, inputs []mediaInput, r Range, out string) error {
	return renderPreviewOptions(ctx, c, inputs, r, out, ExportRequest{})
}

func renderPreviewOptions(ctx context.Context, c Config, inputs []mediaInput, r Range, out string, request ExportRequest) error {
	selection, err := probeInputSelectionOptions(ctx, c, inputs, request)
	if err != nil {
		return err
	}
	if selection.Video == nil {
		return errUnsupportedStream
	}
	filter, hdr, err := videoRenderFilter(selection.Video, "best", true)
	if err != nil {
		return err
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-filter_threads", "1"}
	for _, input := range inputs {
		if r.StartMS < input.OffsetMS {
			return errUnsupportedStream
		}
		protocols := "file"
		if input.Remote {
			protocols = "http,tcp"
		}
		args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats)
		args = append(args, mediaInputBounds(c)...)
		args = append(args, "-ss", seconds(r.StartMS-input.OffsetMS), "-i", input.Path)
	}
	args = append(args, "-t", seconds(r.EndMS-r.StartMS))
	args = append(args, selection.mapArgs("mp4")...)
	args = append(args, "-vf", filter, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "28", "-maxrate", "1400k", "-bufsize", "2800k", "-pix_fmt", "yuv420p", "-threads", "2", "-c:a", "aac", "-b:a", "96k")
	args = append(args, hdrOutputColorArgs(hdr)...)
	args = append(args, "-movflags", "+faststart", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(previewOutputBytes, 10), out)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return renderedMediaFailure(err, hdr)
	}
	stat, err := os.Stat(out)
	if err != nil {
		return err
	}
	if stat.Size() >= previewOutputBytes {
		return errOutputLimit
	}
	info, duration, err := probe(ctx, c, out, false)
	if err != nil {
		return err
	}
	if err := validateRenderedVideo(info, selection.Video, "best", true, hdr); err != nil {
		return err
	}
	if selection.Audio != nil && info.selectedAudio() == nil {
		return errors.New("preview has no selected audio stream")
	}
	if delta := duration - (r.EndMS - r.StartMS); delta > 350 || delta < -350 {
		return errors.New("preview duration does not match requested interval")
	}
	width, height, err := videoDisplayGeometry(selection.Video)
	if err != nil {
		return err
	}
	return appendPreviewGeometry(out, width, height)
}
