package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

const mediaFormats = mediaexport.InputFormats
const maxMediaPixels = mediainfo.MaxPixels
const maxMediaStreams = mediainfo.MaxStreams

// These are per-tool input/decoder bounds, alongside container memory limits.
// Limit stream discovery too: the service only uses one video/audio pair.
func mediaInputBounds(c Config) []string {
	return mediaexport.InputBounds(c.FFmpegThreads)
}

var errProcessResponseTooLarge = mediaerror.ErrProcessResponseTooLarge

// Cancel the process as soon as its response crosses the bound. Merely
// discarding excess stdout bounds RAM but lets a broken extractor consume CPU
// until the whole inspection timeout, or forever for a local caller.
type mediaProcessFailure struct {
	cause    error
	category string
}

func (e *mediaProcessFailure) Error() string {
	return fmt.Sprintf("media process failed [%s]: %v", e.category, e.cause)
}
func (e *mediaProcessFailure) Unwrap() error { return e.cause }

func mediaFailureCategory(stderr string) string { return localmedia.FailureCategory(stderr) }

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	data, err := localmedia.RunCommand(ctx, path, args...)
	return data, localMediaError(err)
}

func runCommandOutput(ctx context.Context, path string, args []string, stdout io.Writer) error {
	return localMediaError(localmedia.RunCommandOutput(ctx, path, args, stdout))
}

type probeInfo mediainfo.Info

func probe(ctx context.Context, c Config, path string, remote bool) (probeInfo, int64, error) {
	p, d, e := localmedia.Probe(ctx, c, path, remote)
	return probeInfo(p), d, localMediaError(e)
}

// HLS staging can have no finite duration until timestamps have been remuxed.
// Stream selection still uses the same bounded probe and validation as files.
func probeStreams(ctx context.Context, c Config, path string, remote bool) (probeInfo, error) {
	p, e := localmedia.ProbeStreams(ctx, c, path, remote)
	return probeInfo(p), localMediaError(e)
}

const platformStreamSelectionVersion = 1

type platformInfo struct {
	cachedUntil            time.Time
	StreamSelectionVersion int              `json:"_cutmy_stream_selection_version,omitempty"`
	ID                     string           `json:"id"`
	Title                  string           `json:"title"`
	Duration               float64          `json:"duration"`
	Extractor              string           `json:"extractor_key"`
	Thumbnail              string           `json:"thumbnail"`
	IsLive                 bool             `json:"is_live"`
	LiveStatus             string           `json:"live_status"`
	Type                   string           `json:"_type"`
	Entries                json.RawMessage  `json:"entries"`
	HasDRM                 bool             `json:"has_drm"`
	Formats                []platformFormat `json:"formats"`
}
type platformFormat struct {
	ID                 string            `json:"format_id"`
	URL                string            `json:"url"`
	Protocol           string            `json:"protocol"`
	VCodec             string            `json:"vcodec"`
	ACodec             string            `json:"acodec"`
	Language           string            `json:"language"`
	LanguagePreference float64           `json:"language_preference"`
	Height             int               `json:"height"`
	Width              int               `json:"width"`
	ABR                float64           `json:"abr"`
	TBR                float64           `json:"tbr"`
	Ext                string            `json:"ext"`
	HasDRM             bool              `json:"has_drm"`
	Headers            map[string]string `json:"http_headers"`
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
	info.StreamSelectionVersion = platformStreamSelectionVersion
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
	if quality == "480p" {
		capHeight = 480
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
			if preferredPlatformAudio(f, audio) {
				audio = f
			}
			if !isHLS(*f) && preferredPlatformAudio(f, progressiveAudio) {
				progressiveAudio = f
			}
		}
	}
	// Rank complete, supported inputs. A high-bitrate HLS video-only rendition
	// must not hide a progressive video/audio pair from the same recording.
	// Separate HLS clocks remain unsupported; no rendition is silently merged.
	var supported []*platformFormat
	preferOriginal := false
	for _, f := range candidates {
		if f.ACodec == "none" && (format == "mp3" || isHLS(*f) || progressiveAudio == nil) {
			continue
		}
		supported = append(supported, f)
		if strings.EqualFold(info.Extractor, "TwitchClips") && !strings.HasPrefix(f.ID, "portrait-") {
			preferOriginal = true
		}
	}
	var video, fallbackVideo *platformFormat
	// Compare the audio actually carried by each supported input (or pair).
	// The extractor ranks the original above default/dubbed tracks, regardless
	// of language. Resolution and bitrate must not switch that soundtrack.
	audioPreference := func(f *platformFormat) float64 {
		if f.ACodec == "none" && progressiveAudio != nil {
			return progressiveAudio.LanguagePreference
		}
		return f.LanguagePreference
	}
	var preferredAudio *platformFormat
	for _, f := range supported {
		if preferOriginal && strings.HasPrefix(f.ID, "portrait-") {
			continue
		}
		if preferredAudio == nil || audioPreference(f) > audioPreference(preferredAudio) {
			preferredAudio = f
		}
	}
	for _, f := range supported {
		// Twitch exposes separately cropped portrait adaptations. Its extractor
		// sorts those first with quality=-2, so resolution ties must not select
		// them over the original edit. Keep this family preference before the
		// quality cap, while allowing portrait-only or unsupported-original clips.
		if preferOriginal && strings.HasPrefix(f.ID, "portrait-") {
			continue
		}
		if preferredAudio != nil && audioPreference(f) < audioPreference(preferredAudio) {
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
		if video != nil && video.ACodec != "none" && (audio == nil || video.LanguagePreference > audio.LanguagePreference) {
			return []platformFormat{*video}, nil
		}
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

func preferredPlatformAudio(candidate, current *platformFormat) bool {
	return current == nil || candidate.LanguagePreference > current.LanguagePreference ||
		candidate.LanguagePreference == current.LanguagePreference && candidate.ABR > current.ABR
}

func platformSourceDimensions(info platformInfo) (int, int) {
	streams, err := pickStreams(info, "best", "mp4")
	if err != nil || len(streams) == 0 {
		return 0, 0
	}
	// Preserve unknown dimensions from the extractor. The resource admission
	// guard stays conservative until real media inspection can establish them.
	return max(0, streams[0].Width), max(0, streams[0].Height)
}

func seconds(ms int64) string { return mediaexport.Seconds(ms) }

type copyKeyframe = mediaexport.Keyframe

func nearestKeyframe(ctx context.Context, c Config, path string, start int64, remote bool, streamIndex int) (copyKeyframe, error) {
	frame, e := localmedia.NearestKeyframe(ctx, c, path, start, remote, streamIndex)
	return frame, localMediaError(e)
}

func keyframeProbeBounds(start int64) (from, until int64) {
	return mediaexport.KeyframeProbeBounds(start)
}

func probeKeyframes(ctx context.Context, c Config, path string, remote bool, streamIndex int, from, until int64) ([]copyKeyframe, error) {
	frames, e := localmedia.ProbeKeyframes(ctx, c, path, remote, streamIndex, from, until)
	return frames, localMediaError(e)
}

func selectCopyKeyframe(frames []copyKeyframe, start int64) (copyKeyframe, error) {
	return mediaexport.SelectCopyKeyframe(frames, start)
}

type mediaInput = mediaexport.Input

func exportMedia(ctx context.Context, c Config, paths []string, remote bool, r Range, request ExportRequest, out string) (int64, int64, error) {
	start, end, e := localmedia.ExportMedia(ctx, c, paths, remote, r, request, out)
	return start, end, localMediaError(e)
}
func exportInputs(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string) (int64, int64, error) {
	start, end, e := localmedia.ExportInputs(ctx, c, inputs, r, request, out)
	return start, end, localMediaError(e)
}

// Intermediate MP4s are read locally by a later mux. Only the final result
// needs faststart's second pass moving its index to the front of the file.
func exportInputsCoreProgressMode(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64), finalOutput bool) (int64, int64, error) {
	return exportInputsCoreVerifiedProgress(ctx, c, inputs, r, request, out, report, finalOutput, nil)
}

func exportInputsCoreVerifiedProgress(ctx context.Context, c Config, inputs []mediaInput, r Range, request ExportRequest, out string, report func(int64), finalOutput bool, verified *probeInfo) (int64, int64, error) {
	start, end, e := localmedia.ExportInputsCoreVerifiedProgress(ctx, c, inputs, r, request, out, report, finalOutput, (*mediainfo.Info)(verified))
	return start, end, localMediaError(e)
}

var errSourceTooLarge = errors.New("source exceeds size limit")

func copyBounded(dst io.Writer, src io.Reader, limit int64) error {
	if limit < 0 {
		return errSourceTooLarge
	}
	// Do not add one to an operator-supplied limit: MaxInt64 + 1 wraps into a
	// negative LimitReader budget and silently accepts an empty source.
	n, err := io.Copy(dst, io.LimitReader(src, limit))
	if err != nil {
		return err
	}
	if n < limit {
		return nil
	}
	var extra [1]byte
	read, err := io.ReadFull(src, extra[:])
	if read > 0 {
		return errSourceTooLarge
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func makeSourceFile(c Config, id string) (*os.File, string, error) {
	dir := filepath.Join(c.DataDir, "sources")
	if err := fsdurable.EnsureDirectory(dir, 0700); err != nil {
		return nil, "", err
	}
	if err := fsdurable.Preflight(dir, dir); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, id+".media.part")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	return f, path, err
}
