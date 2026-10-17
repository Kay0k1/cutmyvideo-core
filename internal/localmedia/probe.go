package localmedia

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func probe(ctx context.Context, c Config, path string, remote bool) (probeInfo, int64, error) {
	p, err := probeStreams(ctx, c, path, remote)
	if err != nil {
		return p, 0, err
	}
	duration, err := (*mediainfo.Info)(&p).DurationMS()
	return p, duration, err
}

func probeStreams(ctx context.Context, c Config, path string, remote bool) (probeInfo, error) {
	if session := localExportSession(ctx, path, remote); session != nil {
		return session.inspect(ctx, c)
	}
	return probeStreamsUncached(ctx, c, path, remote)
}

func probeStreamsUncached(ctx context.Context, c Config, path string, remote bool) (probeInfo, error) {
	protocols := "file"
	if remote {
		protocols = "http,tcp"
	}
	// Request named selection/rendering fields only, not arbitrary tags or
	// complete side data such as a user-controlled display-matrix dump.
	args := []string{"-v", "error", "-max_alloc", "268435456", "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-show_library_versions", "-show_entries", mediaProbeEntries+":library_version=name,major", "-show_data_hash", "sha256", "-of", "json", path)
	b, err := runCommand(ctx, c.FFprobe, args...)
	var p probeInfo
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, p.validateStreams()
}

func nearestKeyframe(ctx context.Context, c Config, path string, start int64, remote bool, streamIndex int) (copyKeyframe, error) {
	if session := localExportSession(ctx, path, remote); session != nil {
		return session.nearestKeyframe(ctx, c, start, streamIndex)
	}
	from, until := keyframeProbeBounds(start)
	frames, err := probeKeyframes(ctx, c, path, remote, streamIndex, from, until)
	if err != nil {
		return copyKeyframe{}, err
	}
	return selectCopyKeyframe(frames, start)
}

func keyframeProbeBounds(start int64) (from, until int64) {
	return mediaexport.KeyframeProbeBounds(start)
}

func probeKeyframes(ctx context.Context, c Config, path string, remote bool, streamIndex int, from, until int64) ([]copyKeyframe, error) {
	protocols := "file"
	if remote {
		protocols = "http,tcp"
	}
	args := []string{"-v", "error", "-max_alloc", "268435456", "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-select_streams", strconv.Itoa(streamIndex), "-read_intervals", seconds(from)+"%"+seconds(until), "-show_packets", "-show_entries", "packet=pts_time,dts_time,flags", "-of", "json", path)
	b, err := runCommand(ctx, c.FFprobe, args...)
	if err != nil {
		return nil, err
	}
	var p struct {
		Packets []struct {
			PTS   string `json:"pts_time"`
			DTS   string `json:"dts_time"`
			Flags string `json:"flags"`
		} `json:"packets"`
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	var frames []copyKeyframe
	for _, v := range p.Packets {
		if !strings.Contains(v.Flags, "K") {
			continue
		}
		n, e := strconv.ParseFloat(v.PTS, 64)
		if e != nil {
			continue
		}
		ms := int64(math.Round(n * 1000))
		if ms >= 0 {
			frame := copyKeyframe{PTS: ms}
			if dts, err := strconv.ParseFloat(v.DTS, 64); err == nil && !math.IsNaN(dts) && !math.IsInf(dts, 0) && math.Abs(dts-n) <= 2 {
				// Rounding DTS upwards can discard the very keyframe we selected.
				frame.DTS, frame.HasDTS = int64(math.Floor(dts*1000)), true
			}
			frames = append(frames, frame)
		}
	}
	return frames, nil
}

func selectCopyKeyframe(frames []copyKeyframe, start int64) (copyKeyframe, error) {
	return mediaexport.SelectCopyKeyframe(frames, start)
}
func Probe(ctx context.Context, c Config, path string, remote bool) (mediainfo.Info, int64, error) {
	p, d, e := probe(ctx, c, path, remote)
	return mediainfo.Info(p), d, e
}
func ProbeStreams(ctx context.Context, c Config, path string, remote bool) (mediainfo.Info, error) {
	p, e := probeStreams(ctx, c, path, remote)
	return mediainfo.Info(p), e
}
func ProbeStreamsUncached(ctx context.Context, c Config, path string, remote bool) (mediainfo.Info, error) {
	p, e := probeStreamsUncached(ctx, c, path, remote)
	return mediainfo.Info(p), e
}
func NearestKeyframe(ctx context.Context, c Config, path string, start int64, remote bool, streamIndex int) (copyKeyframe, error) {
	return nearestKeyframe(ctx, c, path, start, remote, streamIndex)
}
func ProbeKeyframes(ctx context.Context, c Config, path string, remote bool, streamIndex int, from, until int64) ([]copyKeyframe, error) {
	return probeKeyframes(ctx, c, path, remote, streamIndex, from, until)
}
