package app

import (
	"context"
	"encoding/json"
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
	if len(a.Streams) != len(b.Streams) {
		return false
	}
	for i, first := range a.Streams {
		next := b.Streams[i]
		if first.CodecType != next.CodecType || first.CodecName != next.CodecName || first.Width != next.Width || first.Height != next.Height || first.SampleRate != next.SampleRate || first.Channels != next.Channels || first.TimeBase != next.TimeBase || first.ExtradataHash != next.ExtradataHash {
			return false
		}
	}
	return true
}

// A Matroska stream header can round its start to zero while its first packet
// still carries encoder delay. Anchor each period to a real presentation time.
func hlsPacketAnchor(ctx context.Context, c Config, path string, info probeInfo) (int64, error) {
	selector := "a:0"
	for _, stream := range info.Streams {
		if stream.CodecType == "video" {
			selector = "v:0"
			break
		}
	}
	args := []string{"-v", "error", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", "matroska,webm"}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-select_streams", selector, "-read_intervals", "%+#1", "-show_packets", "-show_entries", "packet=pts_time", "-of", "json", path)
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
		path, _, err := stageHLSGroup(ctx, c, guard, format, group, groupDir, stream, downloaded, min(limit, downloaded.Load()+remaining), remaining, func(done, _ int) {
			if progress != nil {
				progress(processed+done, total)
			}
		})
		if err != nil {
			return "", 0, err
		}
		info, _, err := probe(ctx, c, path, false)
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
		anchor, err := hlsPacketAnchor(ctx, c, path, info)
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
		fmt.Fprintf(&list, "file %s/stream-%d.mkv\ninpoint %s\nduration %s\n", name, stream, seconds(anchor), seconds(group[len(group)-1].EndMS-group[0].StartMS))
		processed += len(group)
	}
	manifest := filepath.Join(dir, fmt.Sprintf("periods-%d.ffconcat", stream))
	if err := os.WriteFile(manifest, []byte(list.String()), 0600); err != nil {
		return "", 0, err
	}
	temporary = append(temporary, manifest)
	output := filepath.Join(dir, fmt.Sprintf("stream-%d.mkv", stream))
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(output)
		}
	}()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", "concat,matroska,webm"}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-f", "concat", "-safe", "1", "-i", manifest, "-map", "0:v:0?", "-map", "0:a:0?", "-c", "copy", "-avoid_negative_ts", "make_zero", "-map_metadata", "-1", "-fs", strconv.FormatInt(limit, 10), output)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return "", 0, err
	}
	info, duration, err := probe(ctx, c, output, false)
	last := groups[len(groups)-1]
	expected := last[len(last)-1].EndMS - groups[0][0].StartMS
	if err != nil || math.Abs(float64(duration-expected)) > 1000 || !sameHLSCodecs(firstInfo, info) {
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
