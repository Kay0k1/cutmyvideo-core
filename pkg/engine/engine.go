// Package engine provides local, manually controlled media inspection and
// export without HTTP, PostgreSQL, accounts, or a hosted service.
package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
)

type Config struct {
	FFmpegPath, FFprobePath string
	MaxOutputBytes          int64
	// EncodeProfile accepts "fast" (default) or "compact"; both preserve
	// requested resolution/frame rate. Fast spends fewer CPU cycles and yields
	// larger files. FFmpegThreads defaults to 2 and is limited to 32.
	EncodeProfile string
	FFmpegThreads int
	// Timeout bounds include inspection; an earlier caller deadline wins.
	// Zero uses 2 minutes for inspection and 30 minutes for a whole export.
	InspectTimeout, ExportTimeout time.Duration
}
type Engine struct {
	config         app.Config
	configErr      error
	inspectTimeout time.Duration
	exportTimeout  time.Duration
}
type Range struct{ StartMS, EndMS int64 }
type Options struct{ Format, Quality, CutMode string }
type SourceInfo struct {
	DurationMS int64 `json:"duration_ms"`
	Width      int   `json:"width,omitempty"`
	Height     int   `json:"height,omitempty"`
}
type Result struct {
	Path          string `json:"path"`
	SizeBytes     int64  `json:"size_bytes"`
	ActualStartMS int64  `json:"actual_start_ms"`
	ActualEndMS   int64  `json:"actual_end_ms"`
}

func New(c Config) *Engine {
	if c.FFmpegPath == "" {
		c.FFmpegPath = "ffmpeg"
	}
	if c.FFprobePath == "" {
		c.FFprobePath = "ffprobe"
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = 10 << 30
	}
	if c.EncodeProfile == "" {
		c.EncodeProfile = "fast"
	}
	if c.FFmpegThreads == 0 {
		c.FFmpegThreads = 2
	}
	if c.InspectTimeout == 0 {
		c.InspectTimeout = 2 * time.Minute
	}
	if c.ExportTimeout == 0 {
		c.ExportTimeout = 30 * time.Minute
	}
	e := &Engine{config: app.Config{FFmpeg: c.FFmpegPath, FFprobe: c.FFprobePath, FFmpegProfile: c.EncodeProfile, FFmpegThreads: c.FFmpegThreads, MaxOutputBytes: c.MaxOutputBytes, MaxRanges: 1, MaxRangeMS: 24 * 3600000, MaxJobMS: 24 * 3600000}, inspectTimeout: c.InspectTimeout, exportTimeout: c.ExportTimeout}
	if c.MaxOutputBytes < 0 || c.FFmpegThreads < 1 || c.FFmpegThreads > 32 || (c.EncodeProfile != "fast" && c.EncodeProfile != "compact") || c.InspectTimeout <= 0 || c.ExportTimeout <= 0 {
		e.configErr = errors.New("invalid engine limits, profile, threads, or timeout")
	}
	return e
}

func (e *Engine) Inspect(ctx context.Context, path string) (SourceInfo, error) {
	if e.configErr != nil {
		return SourceInfo{}, e.configErr
	}
	if path == "" {
		return SourceInfo{}, errors.New("input path is required")
	}
	if err := ctx.Err(); err != nil {
		return SourceInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.inspectTimeout)
	defer cancel()
	// An existing relative filename beginning with '-' is not a tool option.
	// Resolving it before ffprobe also avoids dependence on its working directory.
	path, err := filepath.Abs(path)
	if err != nil {
		return SourceInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return SourceInfo{}, errors.New("input must be a readable local file")
	}
	s, err := app.InspectLocal(ctx, e.config, path)
	return SourceInfo{DurationMS: s.DurationMS, Width: s.Width, Height: s.Height}, err
}

func (e *Engine) Export(ctx context.Context, input, output string, r Range, o Options) (Result, error) {
	if e.configErr != nil {
		return Result{}, e.configErr
	}
	if input == "" || output == "" {
		return Result{}, errors.New("input and output paths are required")
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	input, err := filepath.Abs(input)
	if err != nil {
		return Result{}, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return Result{}, err
	}
	s, err := e.Inspect(ctx, input)
	if err != nil {
		return Result{}, err
	}
	if o.Quality == "" {
		o.Quality = "best"
	}
	if o.CutMode == "" {
		o.CutMode = "accurate"
	}
	if o.Format == "" {
		o.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(output)), ".")
	}
	req := app.ExportRequest{Ranges: []app.Range{{StartMS: r.StartMS, EndMS: r.EndMS}}, Format: o.Format, Quality: o.Quality, CutMode: o.CutMode}
	if err = req.Validate(e.config, app.Source{DurationMS: s.DurationMS}); err != nil {
		return Result{}, err
	}
	if _, err = os.Lstat(output); err == nil {
		return Result{}, errors.New("output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(output), ".cutmy-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "output."+o.Format)
	start, end, err := app.ExportLocal(ctx, e.config, input, tmp, req.Ranges[0], req)
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	// Link creates the output exclusively; an existing file is never replaced.
	if err = os.Link(tmp, output); err != nil {
		return Result{}, err
	}
	info, err := os.Stat(output)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: output, SizeBytes: info.Size(), ActualStartMS: start, ActualEndMS: end}, nil
}
