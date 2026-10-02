package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const mediaFormats = "mov,matroska,webm,mp3,wav,flac,ogg,aac,avi"

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		keep := b.limit - b.Len()
		if keep > len(p) {
			keep = len(p)
		}
		_, _ = b.Buffer.Write(p[:keep])
	}
	return n, nil
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	configureProcess(cmd)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	cmd.WaitDelay = 2 * time.Second
	stdout := &limitedBuffer{limit: 8 << 20}
	stderr := &limitedBuffer{limit: 16 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("media process failed: %w", err)
	}
	if stdout.Len() >= stdout.limit {
		return nil, errors.New("process response is too large")
	}
	return stdout.Bytes(), nil
}

type probeInfo struct {
	Format struct {
		Duration  string `json:"duration"`
		StartTime string `json:"start_time"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

func probe(ctx context.Context, c Config, path string, remote bool) (probeInfo, int64, error) {
	protocols := "file"
	if remote {
		protocols = "http,tcp"
	}
	b, err := runCommand(ctx, c.FFprobe, "-v", "error", "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats, "-show_format", "-show_streams", "-of", "json", path)
	var p probeInfo
	if err != nil {
		return p, 0, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, 0, err
	}
	duration, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 30*24*3600 {
		return p, 0, errors.New("source has no supported finite duration")
	}
	if len(p.Streams) == 0 {
		return p, 0, errors.New("source contains no supported streams")
	}
	return p, int64(math.Round(duration * 1000)), nil
}

type platformInfo struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Duration  float64          `json:"duration"`
	Extractor string           `json:"extractor_key"`
	Thumbnail string           `json:"thumbnail"`
	IsLive    bool             `json:"is_live"`
	Formats   []platformFormat `json:"formats"`
}
type platformFormat struct {
	ID       string  `json:"format_id"`
	URL      string  `json:"url"`
	Protocol string  `json:"protocol"`
	VCodec   string  `json:"vcodec"`
	ACodec   string  `json:"acodec"`
	Height   int     `json:"height"`
	Width    int     `json:"width"`
	ABR      float64 `json:"abr"`
	TBR      float64 `json:"tbr"`
	Ext      string  `json:"ext"`
}

func platformMetadata(ctx context.Context, c Config, raw string, g *networkGuard) (platformInfo, error) {
	if _, err := validateURL(raw); err != nil {
		return platformInfo{}, err
	}
	b, err := runCommand(ctx, c.YTDLP, "--ignore-config", "--no-plugin-dirs", "--no-remote-components", "--no-js-runtimes", "--js-runtimes", "node", "--no-playlist", "--no-warnings", "--socket-timeout", "15", "--retries", "1", "--extractor-retries", "1", "--proxy", g.ProxyURL(), "--skip-download", "--dump-single-json", "--", raw)
	var info platformInfo
	if err != nil {
		return info, err
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	if info.IsLive || info.Duration <= 0 || math.IsNaN(info.Duration) || math.IsInf(info.Duration, 0) || info.Duration > 30*24*3600 {
		return info, errors.New("live streams and sources without duration are not supported")
	}
	return info, nil
}

func pickStreams(info platformInfo, quality, format string) ([]platformFormat, error) {
	capHeight := 100000
	if quality == "1080p" {
		capHeight = 1080
	}
	if quality == "720p" {
		capHeight = 720
	}
	var video, audio *platformFormat
	for i := range info.Formats {
		f := &info.Formats[i]
		if f.Protocol != "https" || (f.Ext != "mp4" && f.Ext != "webm" && f.Ext != "m4a" && f.Ext != "mp3") {
			continue
		}
		if _, err := validateURL(f.URL); err != nil {
			continue
		}
		if f.VCodec != "none" && f.VCodec != "" && f.Height <= capHeight && (video == nil || f.Height > video.Height || (f.Height == video.Height && f.TBR > video.TBR)) {
			video = f
		}
		if f.ACodec != "none" && f.ACodec != "" && (f.VCodec == "none" || f.VCodec == "") && (audio == nil || f.ABR > audio.ABR) {
			audio = f
		}
	}
	if format == "mp3" {
		if audio != nil {
			return []platformFormat{*audio}, nil
		}
		if video != nil && video.ACodec != "none" && video.ACodec != "" {
			return []platformFormat{*video}, nil
		}
	}
	if format == "mp4" && video != nil {
		if video.ACodec != "none" && video.ACodec != "" {
			return []platformFormat{*video}, nil
		}
		if audio != nil {
			return []platformFormat{*video, *audio}, nil
		}
	}
	return nil, errors.New("no supported seekable HTTPS streams; this source requires an unsupported segmented or authenticated format")
}

func seconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }

func nearestKeyframe(ctx context.Context, c Config, path string, start int64, remote bool) (int64, error) {
	if start == 0 {
		return 0, nil
	}
	from := start - 30000
	if from < 0 {
		from = 0
	}
	protocols := "file"
	if remote {
		protocols = "http,tcp"
	}
	b, err := runCommand(ctx, c.FFprobe, "-v", "error", "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats, "-select_streams", "v:0", "-read_intervals", seconds(from)+"%"+seconds(start+1), "-show_packets", "-show_entries", "packet=pts_time,flags", "-of", "json", path)
	if err != nil {
		return 0, err
	}
	var p struct {
		Packets []struct {
			PTS   string `json:"pts_time"`
			Flags string `json:"flags"`
		} `json:"packets"`
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return 0, err
	}
	var found int64 = -1
	for _, v := range p.Packets {
		if !strings.Contains(v.Flags, "K") {
			continue
		}
		n, e := strconv.ParseFloat(v.PTS, 64)
		if e != nil {
			continue
		}
		ms := int64(math.Round(n * 1000))
		if ms <= start && ms >= 0 && ms > found {
			found = ms
		}
	}
	if found < 0 {
		return 0, errors.New("no nearby keyframe found; use accurate mode")
	}
	return found, nil
}

func exportMedia(ctx context.Context, c Config, inputs []string, remote bool, r Range, request ExportRequest, out string) (int64, int64, error) {
	start := r.StartMS
	if request.CutMode == "copy" {
		var err error
		start, err = nearestKeyframe(ctx, c, inputs[0], start, remote)
		if err != nil {
			return 0, 0, err
		}
	}
	threads := env("FFMPEG_THREADS", "2")
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-filter_threads", threads, "-filter_complex_threads", threads}
	protocols := "file"
	if remote {
		protocols = "http,tcp"
	}
	for _, input := range inputs {
		args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats, "-threads", threads, "-ss", seconds(start), "-i", input)
	}
	args = append(args, "-t", seconds(r.EndMS-start))
	if request.Format == "mp3" {
		args = append(args, "-map", "0:a:0", "-vn", "-c:a", "libmp3lame", "-b:a", "192k")
	} else {
		args = append(args, "-map", "0:v:0")
		if len(inputs) > 1 {
			args = append(args, "-map", "1:a:0")
		} else {
			args = append(args, "-map", "0:a:0?")
		}
		if request.CutMode == "copy" {
			args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero")
		} else {
			args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "192k")
			if request.Quality != "best" {
				height := 1080
				if request.Quality == "720p" {
					height = 720
				}
				args = append(args, "-vf", fmt.Sprintf("scale=-2:'trunc(min(%d,ih)/2)*2'", height))
			} else {
				args = append(args, "-vf", "scale='trunc(iw/2)*2':'trunc(ih/2)*2'")
			}
		}
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-threads", env("FFMPEG_THREADS", "2"), "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(c.MaxOutputBytes, 10), out)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return 0, 0, err
	}
	p, duration, err := probe(ctx, c, out, false)
	if err != nil {
		return 0, 0, err
	}
	if request.Format == "mp4" {
		video := false
		for _, s := range p.Streams {
			if s.CodecType == "video" {
				video = true
			}
		}
		if !video {
			return 0, 0, errors.New("result has no video stream")
		}
	}
	info, err := os.Stat(out)
	if err != nil {
		return 0, 0, err
	}
	if info.Size() >= c.MaxOutputBytes {
		return 0, 0, errors.New("result exceeds output size limit")
	}
	// A duration mismatch also catches FFmpeg's graceful -fs truncation.
	expected := r.EndMS - start
	if request.CutMode == "accurate" && math.Abs(float64(duration-expected)) > 350 {
		return 0, 0, errors.New("result duration does not match the requested range")
	}
	return start, start + duration, nil
}

func copyBounded(dst io.Writer, src io.Reader, limit int64) error {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("source exceeds size limit")
	}
	return nil
}

func makeSourceFile(c Config, id string) (*os.File, string, error) {
	dir := filepath.Join(c.DataDir, "sources")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, id+".media")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	return f, path, err
}
