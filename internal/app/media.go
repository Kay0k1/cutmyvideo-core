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

const mediaFormats = "mov,matroska,webm,mp3,wav,flac,ogg,aac,avi,mpegts"

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

type mediaProcessFailure struct {
	cause    error
	category string
}

func (e *mediaProcessFailure) Error() string {
	return fmt.Sprintf("media process failed [%s]: %v", e.category, e.cause)
}
func (e *mediaProcessFailure) Unwrap() error { return e.cause }

// Only fixed categories leave the subprocess boundary. Raw diagnostics may
// contain signed CDN URLs, proxy credentials, or user-controlled file names.
// A category is diagnostic evidence, never permission to retry an export.
func mediaFailureCategory(stderr string) string {
	text := strings.ToLower(stderr)
	if strings.Contains(text, "stream map") && strings.Contains(text, ":a:") && strings.Contains(text, "matches no streams") {
		return "missing_audio"
	}
	if strings.Contains(text, "no space left on device") {
		return "storage_full"
	}
	if strings.Contains(text, "not currently supported in container") || strings.Contains(text, "codec not supported in container") {
		return "copy_incompatible"
	}
	for _, phrase := range []string{"login required", "login_required", "sign in to", "log in to", "only available for registered", "subscriber-only", "subscribers only", "private video", "password protected"} {
		if strings.Contains(text, phrase) {
			return "platform_access"
		}
	}
	for _, status := range []string{"401", "403", "404", "410"} {
		if strings.Contains(text, "http error "+status) || strings.Contains(text, "server returned "+status) {
			return "upstream_denied"
		}
	}
	for _, status := range []string{"408", "429", "500", "502", "503", "504"} {
		if strings.Contains(text, "http error "+status) || strings.Contains(text, "server returned "+status) {
			return "upstream_unavailable"
		}
	}
	if strings.Contains(text, "connection timed out") || strings.Contains(text, "operation timed out") {
		return "network_timeout"
	}
	if strings.Contains(text, "connection reset by peer") {
		return "network_reset"
	}
	if strings.Contains(text, "invalid data found when processing input") || strings.Contains(text, "unknown decoder") || strings.Contains(text, "unknown encoder") || strings.Contains(text, "not on whitelist") {
		return "unsupported_media"
	}
	return "unknown"
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
		return nil, &mediaProcessFailure{cause: err, category: mediaFailureCategory(stderr.String())}
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
		StartTime string `json:"start_time"`
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
	ID         string           `json:"id"`
	Title      string           `json:"title"`
	Duration   float64          `json:"duration"`
	Extractor  string           `json:"extractor_key"`
	Thumbnail  string           `json:"thumbnail"`
	IsLive     bool             `json:"is_live"`
	LiveStatus string           `json:"live_status"`
	Type       string           `json:"_type"`
	Entries    json.RawMessage  `json:"entries"`
	HasDRM     bool             `json:"has_drm"`
	Formats    []platformFormat `json:"formats"`
}
type platformFormat struct {
	ID       string            `json:"format_id"`
	URL      string            `json:"url"`
	Protocol string            `json:"protocol"`
	VCodec   string            `json:"vcodec"`
	ACodec   string            `json:"acodec"`
	Height   int               `json:"height"`
	Width    int               `json:"width"`
	ABR      float64           `json:"abr"`
	TBR      float64           `json:"tbr"`
	Ext      string            `json:"ext"`
	HasDRM   bool              `json:"has_drm"`
	Headers  map[string]string `json:"http_headers"`
}

func platformMetadata(ctx context.Context, c Config, raw string, g *networkGuard) (platformInfo, error) {
	if _, err := validateURL(raw); err != nil {
		return platformInfo{}, err
	}
	b, err := runCommand(ctx, c.YTDLP, platformMetadataArgs(raw, g.ProxyURL())...)
	var info platformInfo
	if err != nil {
		var failure *mediaProcessFailure
		if errors.As(err, &failure) {
			if failure.category == "platform_access" {
				return info, errPlatformAccess
			}
			return info, errPlatformUnavailable
		}
		return info, err
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	return info, validatePlatformInfo(info)
}

func platformMetadataArgs(raw, proxy string) []string {
	// Caption URL matrices are unused and can exceed the subprocess JSON limit.
	// Run before simulation; keep the complete root envelope and stream metadata.
	return []string{"--ignore-config", "--no-plugin-dirs", "--no-remote-components", "--no-js-runtimes", "--js-runtimes", "node", "--no-playlist", "--playlist-end", "1", "--no-warnings", "--socket-timeout", "15", "--retries", "1", "--extractor-retries", "1", "--proxy", proxy, "--parse-metadata", "pre_process::(?P<automatic_captions>)(?P<subtitles>)", "--skip-download", "--dump-single-json", "--", raw}
}

func validatePlatformInfo(info platformInfo) error {
	if info.Type == "playlist" || info.Type == "multi_video" || len(info.Entries) > 0 && string(info.Entries) != "null" {
		return errCollection
	}
	if info.IsLive || info.LiveStatus == "is_live" || info.LiveStatus == "is_upcoming" || info.LiveStatus == "post_live" {
		return errLiveSource
	}
	if info.HasDRM {
		return errUnsupportedStream
	}
	if info.ID == "" || info.Title == "" || info.Duration <= 0 || math.IsNaN(info.Duration) || math.IsInf(info.Duration, 0) || info.Duration > 30*24*3600 {
		return errPlatformUnavailable
	}
	return nil
}

func pickStreams(info platformInfo, quality, format string) ([]platformFormat, error) {
	capHeight := 100000
	if quality == "1080p" {
		capHeight = 1080
	}
	if quality == "720p" {
		capHeight = 720
	}
	var audio, progressiveAudio *platformFormat
	var candidates []*platformFormat
	for i := range info.Formats {
		f := &info.Formats[i]
		if f.HasDRM || (f.Protocol != "https" && !isHLS(*f)) || (f.Ext != "mp4" && f.Ext != "webm" && f.Ext != "m4a" && f.Ext != "mp3" && f.Ext != "ts") {
			continue
		}
		if _, err := validateURL(f.URL); err != nil {
			continue
		}
		// An omitted codec is unknown, not the extractor's explicit "none".
		// Public progressive clips (notably Twitch) often omit both codec fields.
		hasVideo := f.VCodec != "none" && (f.VCodec != "" || f.Height > 0 || f.Ext == "mp4" || f.Ext == "webm")
		if hasVideo {
			candidates = append(candidates, f)
		}
		if f.ACodec != "none" && (f.VCodec == "none" || !hasVideo) {
			if audio == nil || f.ABR > audio.ABR {
				audio = f
			}
			if !isHLS(*f) && (progressiveAudio == nil || f.ABR > progressiveAudio.ABR) {
				progressiveAudio = f
			}
		}
	}
	// Rank complete, supported inputs. A high-bitrate HLS video-only rendition
	// must not hide a progressive video/audio pair from the same recording.
	// Separate HLS clocks remain unsupported; no rendition is silently merged.
	var video, fallbackVideo *platformFormat
	for _, f := range candidates {
		if f.ACodec == "none" && (format == "mp3" || isHLS(*f) || progressiveAudio == nil) {
			continue
		}
		if f.Height <= capHeight && (video == nil || f.Height > video.Height || f.Height == video.Height && f.TBR > video.TBR) {
			video = f
		}
		if f.Height > capHeight && (fallbackVideo == nil || f.Height < fallbackVideo.Height || f.Height == fallbackVideo.Height && f.TBR < fallbackVideo.TBR) {
			fallbackVideo = f
		}
	}
	if video == nil {
		video = fallbackVideo
	}
	if format == "mp3" {
		if audio != nil {
			return []platformFormat{*audio}, nil
		}
		if video != nil && video.ACodec != "none" {
			return []platformFormat{*video}, nil
		}
		if len(candidates) > 0 {
			return nil, errNoAudio
		}
	}
	if format == "mp4" && video != nil {
		if video.ACodec != "none" {
			return []platformFormat{*video}, nil
		}
		if progressiveAudio != nil && !isHLS(*video) {
			return []platformFormat{*video, *progressiveAudio}, nil
		}
	}
	return nil, errUnsupportedStream
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

type mediaInput struct {
	Path     string
	Remote   bool
	OffsetMS int64
}

func exportMedia(ctx context.Context, c Config, paths []string, remote bool, r Range, request ExportRequest, out string) (int64, int64, error) {
	inputs := make([]mediaInput, len(paths))
	for i, path := range paths {
		inputs[i] = mediaInput{Path: path, Remote: remote}
	}
	return exportInputs(ctx, c, inputs, r, request, out)
}
func exportInputs(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string) (int64, int64, error) {
	if len(inputs) == 0 {
		return 0, 0, errUnsupportedStream
	}
	start := r.StartMS
	if request.CutMode == "copy" {
		if request.Quality != "best" {
			info, _, err := probe(ctx, c, inputs[0].Path, inputs[0].Remote)
			if err != nil {
				return 0, 0, err
			}
			capHeight := 1080
			if request.Quality == "720p" {
				capHeight = 720
			}
			for _, stream := range info.Streams {
				if stream.CodecType == "video" && stream.Height > capHeight {
					return 0, 0, &sourceProblem{"copy_quality_unsupported", "Copy mode cannot reduce this video's resolution. Choose original quality or accurate mode"}
				}
			}
		}
		local, err := nearestKeyframe(ctx, c, inputs[0].Path, start-inputs[0].OffsetMS, inputs[0].Remote)
		if err != nil {
			return 0, 0, err
		}
		start = local + inputs[0].OffsetMS
	}

	threads := env("FFMPEG_THREADS", "2")
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-filter_threads", threads, "-filter_complex_threads", threads}
	for _, input := range inputs {
		protocols := "file"
		if input.Remote {
			protocols = "http,tcp"
		}
		localStart := start - input.OffsetMS
		if localStart < 0 {
			return 0, 0, errUnsupportedStream
		}
		args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats, "-threads", threads, "-ss", seconds(localStart), "-i", input.Path)
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
		if info, statErr := os.Stat(out); statErr == nil && info.Size() >= c.MaxOutputBytes {
			return 0, 0, errOutputLimit
		}
		return 0, 0, err
	}
	info, err := os.Stat(out)
	if err != nil {
		return 0, 0, err
	}
	if info.Size() >= c.MaxOutputBytes {
		return 0, 0, errOutputLimit
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
