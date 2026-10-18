package localmedia

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

const PosterMaxPixels = 16_777_216
const PosterMaxBytes int64 = 16 << 20
const PosterTimeout = 30 * time.Second

type PosterOptions struct {
	TimestampMS int64  `json:"timestamp_ms"`
	Format      string `json:"format,omitempty"`
}

type PosterResult struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	TimestampMS int64  `json:"timestamp_ms"`
	Format      string `json:"format"`
}

var ErrInvalidPoster = errors.New("invalid poster options or video frame")
var ErrPosterTooLarge = errors.New("poster exceeds image limits")
var ErrPosterFrameUnavailable = fmt.Errorf("%w: no decodable frame at or after timestamp", ErrInvalidPoster)

func NormalizePosterOptions(o PosterOptions) (PosterOptions, error) {
	if o.Format == "" {
		o.Format = "jpeg"
	}
	if o.TimestampMS < 0 || (o.Format != "jpeg" && o.Format != "png") {
		return o, ErrInvalidPoster
	}
	return o, nil
}

type posterWriter struct {
	io.Writer
	remaining int64
	overflow  bool
	cancel    context.CancelFunc
}

func (w *posterWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		w.overflow = true
		w.cancel()
		return 0, ErrPosterTooLarge
	}
	n, err := w.Writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// GeneratePoster extracts one frame from a finite local finished video. It
// creates output exclusively; callers publish it only after verification.
func GeneratePoster(parent context.Context, c Config, input, output string, options PosterOptions) (result PosterResult, err error) {
	o, err := NormalizePosterOptions(options)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(parent, PosterTimeout)
	defer cancel()
	info, err := os.Stat(input)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, ErrInvalidPoster
	}
	p, duration, err := probe(ctx, c, input, false)
	if err != nil {
		return result, err
	}
	v := (*mediainfo.Info)(&p).SelectedVideo()
	if v == nil || o.TimestampMS >= duration {
		return result, ErrInvalidPoster
	}
	w, h, err := mediainfo.DisplayGeometry(v)
	if err != nil || v.Height <= 0 || v.Width > PosterMaxPixels/v.Height || h <= 0 || w > PosterMaxPixels/h {
		return result, ErrPosterTooLarge
	}
	filter, _, err := mediainfo.ColorRenderFilter(v, fmt.Sprintf("scale=%d:%d,setsar=1", w, h))
	if err != nil {
		return result, err
	}
	limit := PosterMaxBytes
	if c.MaxOutputBytes > 0 {
		limit = min(limit, c.MaxOutputBytes)
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return result, err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(output)
		}
	}()
	args := []string{"-v", "error", "-nostdin", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-ss", seconds(o.TimestampMS), "-i", input, "-map", "0:"+strconv.Itoa(v.Index), "-an", "-sn", "-dn", "-vf", filter, "-frames:v", "1", "-threads", "1")
	if o.Format == "jpeg" {
		args = append(args, "-c:v", "mjpeg", "-q:v", "2", "-pix_fmt", "yuvj420p")
	} else {
		args = append(args, "-c:v", "png", "-pix_fmt", "rgb24")
	}
	args = append(args, "-f", "image2pipe", "pipe:1")
	writer := &posterWriter{Writer: f, remaining: limit, cancel: cancel}
	if err = runCommandOutput(ctx, c.FFmpeg, args, writer); err != nil {
		if parent.Err() != nil {
			return result, parent.Err()
		}
		if writer.overflow {
			return result, ErrPosterTooLarge
		}
		if writer.remaining == 0 || ctx.Err() != nil && parent.Err() == nil {
			return result, errors.Join(err, ctx.Err())
		}
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	after, statErr := os.Stat(input)
	if statErr != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return result, &mediaerror.Problem{Code: "source_changed", Message: "poster input changed during generation"}
	}
	stat, err := f.Stat()
	if err != nil {
		return result, err
	}
	if stat.Size() <= 0 {
		return result, ErrPosterFrameUnavailable
	}
	if stat.Size() > limit {
		return result, ErrPosterTooLarge
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	config, format, err := image.DecodeConfig(f)
	if err != nil || format != o.Format || config.Width != w || config.Height != h {
		return result, ErrInvalidPoster
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	decoded, format, err := image.Decode(f)
	if err != nil || format != o.Format || decoded.Bounds().Dx() != w || decoded.Bounds().Dy() != h {
		return result, ErrInvalidPoster
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = f.Sync(); err != nil {
		return result, err
	}
	return PosterResult{Path: output, SizeBytes: stat.Size(), Width: w, Height: h, TimestampMS: o.TimestampMS, Format: o.Format}, nil
}
