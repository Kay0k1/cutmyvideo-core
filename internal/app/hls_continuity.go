package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

// A restart may reset timestamps and replace the fMP4 initialization map.
// Each continuity period must be remuxed with its own initialization bytes.
func splitHLSContinuity(segments []hlsSegment) ([][]hlsSegment, error) {
	var groups [][]hlsSegment
	start := 0
	for i := 1; i < len(segments); i++ {
		if segments[i].Discontinuity || segments[i].MapURL != segments[i-1].MapURL {
			groups = append(groups, segments[start:i])
			start = i
		}
	}
	groups = append(groups, segments[start:])
	if len(segments) == 0 || len(groups) > 64 {
		return nil, errUnsupportedStream
	}
	return groups, nil
}

func sameHLSCodecs(a, b probeInfo) bool {
	return sameHLSStream(a.selectedVideo(), b.selectedVideo()) && sameHLSStream(a.selectedAudio(), b.selectedAudio())
}

func sameHLSStream(a, b *probeStream) bool {
	if a == nil || b == nil {
		return a == b
	}
	// Index and disposition can change during remuxing. Only the selected
	// streams' codec, presentation geometry and colour properties must agree.
	return a.CodecType == b.CodecType && a.CodecName == b.CodecName && a.Width == b.Width && a.Height == b.Height && a.SampleRate == b.SampleRate && a.Channels == b.Channels && a.TimeBase == b.TimeBase && a.ExtradataHash == b.ExtradataHash && a.SampleAspectRatio == b.SampleAspectRatio && a.rotation() == b.rotation() && a.PixelFormat == b.PixelFormat && a.ColorPrimaries == b.ColorPrimaries && a.ColorTransfer == b.ColorTransfer && a.ColorSpace == b.ColorSpace && a.ColorRange == b.ColorRange
}

func hlsStageExtension(video *probeStream) string {
	if video != nil && math.Abs(math.Remainder(video.rotation(), 360)) > 0.001 {
		return "mp4"
	}
	return "mkv"
}

func hlsRemuxFailure(err error, video *probeStream) error {
	var failure *mediaProcessFailure
	if hlsStageExtension(video) == "mp4" && errors.As(err, &failure) && (failure.category == "copy_incompatible" || failure.category == "unsupported_media") {
		return fmt.Errorf("%w: %w", errUnsupportedStream, err)
	}
	return err
}

// Remuxing may renumber selected streams and change their time bases. It must
// retain their coded geometry, display matrix, sample aspect and colour signal.
func sameHLSPresentation(before, after probeInfo) bool {
	a, b := before.selectedVideo(), after.selectedVideo()
	if a == nil || b == nil {
		if a != b {
			return false
		}
	} else if a.CodecName != b.CodecName || a.Width != b.Width || a.Height != b.Height || !sameHLSSAR(a.SampleAspectRatio, b.SampleAspectRatio) || math.Abs(math.Remainder(a.rotation()-b.rotation(), 360)) > 0.001 || a.PixelFormat != b.PixelFormat || a.ColorPrimaries != b.ColorPrimaries || a.ColorTransfer != b.ColorTransfer || a.ColorSpace != b.ColorSpace || a.ColorRange != b.ColorRange {
		return false
	}
	a, b = before.selectedAudio(), after.selectedAudio()
	if a == nil || b == nil {
		return a == b
	}
	return a.CodecName == b.CodecName && a.SampleRate == b.SampleRate && a.Channels == b.Channels
}

func sameHLSSAR(a, b string) bool {
	parse := func(value string) (int64, int64, bool) {
		if value == "" || value == "N/A" || value == "0:1" {
			return 1, 1, true
		}
		numerator, denominator, ok := strings.Cut(value, ":")
		if !ok {
			return 0, 0, false
		}
		n, err := strconv.ParseInt(numerator, 10, 32)
		d, denomErr := strconv.ParseInt(denominator, 10, 32)
		return n, d, err == nil && denomErr == nil && n > 0 && d > 0
	}
	an, ad, aok := parse(a)
	bn, bd, bok := parse(b)
	return aok && bok && an*bd == bn*ad
}

// A Matroska stream header can round its start to zero while its first packet
// still carries encoder delay. Anchor each period to a real presentation time.
func hlsPacketAnchor(ctx context.Context, c Config, path string, info probeInfo) (int64, error) {
	stream := info.selectedVideo()
	if stream == nil {
		stream = info.selectedAudio()
	}
	if stream == nil {
		return 0, errUnsupportedStream
	}
	args := []string{"-v", "error", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", "matroska,webm,mov"}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-select_streams", strconv.Itoa(stream.Index), "-read_intervals", "%+#1", "-show_packets", "-show_entries", "packet=pts_time", "-of", "json", path)
	raw, err := runCommand(ctx, c.FFprobe, args...)
	if err != nil {
		return 0, err
	}
	var result struct {
		Packets []struct {
			PTS string `json:"pts_time"`
		} `json:"packets"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Packets) != 1 {
		return 0, errUnsupportedStream
	}
	value, err := strconv.ParseFloat(result.Packets[0].PTS, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
		return 0, errUnsupportedStream
	}
	return int64(math.Round(value * 1000)), nil
}

// A period's remux permit has already been released. Its packet inspection
// must reacquire the shared decoder budget before starting another probe.
func hlsPreparedPacketAnchor(ctx context.Context, c Config, format platformFormat, path string, info probeInfo) (int64, error) {
	release, err := acquireMediaInspection(ctx, Source{}, []platformFormat{format})
	if err != nil {
		return 0, err
	}
	defer release()
	return hlsPacketAnchor(ctx, c, path, info)
}

func stageHLSGroups(ctx context.Context, c Config, guard *networkGuard, format platformFormat, groups [][]hlsSegment, dir string, stream int, downloaded *atomic.Int64, limit int64, progress func(int, int)) (string, int64, error) {
	var temporary []string
	defer func() {
		for _, path := range temporary {
			_ = os.RemoveAll(path)
		}
	}()
	var list strings.Builder
	list.WriteString("ffconcat version 1.0\n")
	var firstInfo probeInfo
	var stagedBytes int64
	processed, total := 0, 0
	for _, group := range groups {
		total += len(group)
	}
	for index, group := range groups {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		name := fmt.Sprintf("period-%d-%03d", stream, index)
		groupDir := filepath.Join(dir, name)
		if err := os.Mkdir(groupDir, 0700); err != nil {
			return "", 0, err
		}
		temporary = append(temporary, groupDir)
		// Aggregate normalized groups are capped at B; the current raw group
		// shares another B ceiling across every download, including init maps.
		// Concatenation later uses B for its output while retaining <= B inputs.
		remaining := limit - stagedBytes
		if remaining <= 0 {
			return "", 0, errSourceTooLarge
		}
		path, _, info, err := stageHLSGroupWithInfo(ctx, c, guard, format, group, groupDir, stream, downloaded, min(limit, downloaded.Load()+remaining), remaining, func(done, _ int) {
			if progress != nil {
				progress(processed+done, total)
			}
		})
		if err != nil {
			return "", 0, err
		}
		if index == 0 {
			firstInfo = info
		} else if !sameHLSCodecs(firstInfo, info) {
			// A changed SPS/PPS or audio configuration cannot safely be copied
			// into one track. Never publish a superficially valid broken MP4.
			return "", 0, errUnsupportedStream
		}
		anchor, err := hlsPreparedPacketAnchor(ctx, c, format, path, info)
		if err != nil {
			return "", 0, err
		}
		stat, err := os.Stat(path)
		if err != nil {
			return "", 0, err
		}
		stagedBytes += stat.Size()
		if stagedBytes >= limit {
			return "", 0, errSourceTooLarge
		}
		// Only generated ASCII basenames reach this manifest. FFmpeg is never
		// given upstream URLs, playlists or user-controlled filesystem paths.
		fmt.Fprintf(&list, "file %s/%s\ninpoint %s\nduration %s\n", name, filepath.Base(path), seconds(anchor), seconds(group[len(group)-1].EndMS-group[0].StartMS))
		processed += len(group)
	}
	manifest := filepath.Join(dir, fmt.Sprintf("periods-%d.ffconcat", stream))
	if err := os.WriteFile(manifest, []byte(list.String()), 0600); err != nil {
		return "", 0, err
	}
	temporary = append(temporary, manifest)
	output := filepath.Join(dir, fmt.Sprintf("stream-%d.%s", stream, hlsStageExtension(firstInfo.selectedVideo())))
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(output)
		}
	}()
	release, inspectionErr := acquireMediaInspection(ctx, Source{}, []platformFormat{format})
	if inspectionErr != nil {
		return "", 0, inspectionErr
	}
	defer release()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", "concat,matroska,webm,mov"}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-f", "concat", "-safe", "1", "-i", manifest)
	selection := mediaSelection{Video: firstInfo.selectedVideo(), Audio: firstInfo.selectedAudio()}
	args = append(args, selection.mapArgs("mp4")...)
	args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero", "-map_metadata", "-1", "-fs", strconv.FormatInt(limit, 10), output)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return "", 0, hlsRemuxFailure(err, selection.Video)
	}
	info, duration, err := probe(ctx, c, output, false)
	if err != nil {
		return "", 0, err
	}
	last := groups[len(groups)-1]
	expected := last[len(last)-1].EndMS - groups[0][0].StartMS
	if math.Abs(float64(duration-expected)) > 1000 || !sameHLSCodecs(firstInfo, info) || !sameHLSPresentation(firstInfo, info) {
		return "", 0, errUnsupportedStream
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Size() >= limit {
		return "", 0, errSourceTooLarge
	}
	anchor, err := hlsPacketAnchor(ctx, c, output, info)
	if err != nil {
		return "", 0, err
	}
	keep = true
	return output, groups[0][0].StartMS - anchor, nil
}
