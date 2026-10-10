// Package engine provides local, manually controlled media inspection and
// export without HTTP, PostgreSQL, accounts, or a hosted service.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

// Config controls external media tools and resource limits. Zero fields use
// the documented defaults; invalid values fail Inspect and Export.
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

// Engine is immutable after New and safe to share between callers. Each
// operation has its own deadline and temporary output workspace.
type Engine struct {
	config         app.Config
	configErr      error
	inspectTimeout time.Duration
	exportTimeout  time.Duration
	preflight      func(string, string) error
	publish        func(context.Context, string, string) (fs.FileInfo, error)
}

// Range identifies an interval on the original source timeline in milliseconds.
type Range struct{ StartMS, EndMS int64 }

// Options selects mp4/mp3, best/1080p/720p and accurate/copy respectively.
// Empty fields infer the format from the output suffix and use best/accurate.
type Options struct{ Format, Quality, CutMode string }

// SourceInfo describes a finite local media file. Audio files omit dimensions.
type SourceInfo struct {
	DurationMS int64 `json:"duration_ms"`
	Width      int   `json:"width,omitempty"`
	Height     int   `json:"height,omitempty"`
}

// Result describes an exclusively published output. Copy mode can move its
// actual bounds to packet/keyframe boundaries.
type Result struct {
	Path          string `json:"path"`
	SizeBytes     int64  `json:"size_bytes"`
	ActualStartMS int64  `json:"actual_start_ms"`
	ActualEndMS   int64  `json:"actual_end_ms"`
}

// ErrOutputExists means an existing output file or symlink was preserved.
// Callers can detect it with errors.Is, including concurrent publication races.
var ErrOutputExists = errors.New("output already exists")

// New constructs a local engine without opening a database or spawning tools.
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
	e.preflight, e.publish = fsdurable.Preflight, fsdurable.Publish
	if c.MaxOutputBytes < 0 || c.FFmpegThreads < 1 || c.FFmpegThreads > 32 || (c.EncodeProfile != "fast" && c.EncodeProfile != "compact") || c.InspectTimeout <= 0 || c.ExportTimeout <= 0 {
		e.configErr = errors.New("invalid engine limits, profile, threads, or timeout")
	}
	return e
}

// Inspect reads duration and dimensions with FFprobe within InspectTimeout.
// Only regular local files and supported finite media formats are accepted.
func (e *Engine) Inspect(ctx context.Context, path string) (result SourceInfo, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return SourceInfo{}, invalidArgument(e.configErr)
	}
	if path == "" {
		return SourceInfo{}, invalidArgument(errors.New("input path is required"))
	}
	if err := ctx.Err(); err != nil {
		return SourceInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.inspectTimeout)
	defer cancel()
	// An existing relative filename beginning with '-' is not a tool option.
	// Resolving it before ffprobe also avoids dependence on its working directory.
	path, err = filepath.Abs(path)
	if err != nil {
		return SourceInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return SourceInfo{}, &messageCause{message: "input must be a readable local file", cause: err}
	}
	if !info.Mode().IsRegular() {
		return SourceInfo{}, invalidArgument(errors.New("input must be a readable local file"))
	}
	s, err := app.InspectLocal(ctx, e.config, path)
	return SourceInfo{DurationMS: s.DurationMS, Width: s.Width, Height: s.Height}, err
}

// Export validates the interval, writes and verifies a temporary media file,
// then publishes it exclusively at output. The output directory must exist.
// Its containing directory hierarchy must already be persistent; Export syncs
// the output entry, not arbitrary directories recently created by the caller.
// Cancellation and failures before publication remove temporary files. A
// publication_uncertain error means the final output may exist; inspect it
// before retrying. Publication requires hard links and directory sync support.
func (e *Engine) Export(ctx context.Context, input, output string, r Range, o Options) (result Result, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return Result{}, invalidArgument(e.configErr)
	}
	if input == "" || output == "" {
		return Result{}, invalidArgument(errors.New("input and output paths are required"))
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	input, err = filepath.Abs(input)
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
		return Result{}, invalidArgument(err)
	}
	if _, err = os.Lstat(output); err == nil {
		return Result{}, &messageCause{message: ErrOutputExists.Error(), cause: errors.Join(ErrOutputExists, os.ErrExist)}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(output), ".cutmy-")
	if err != nil {
		return Result{}, fmt.Errorf("%w: create output workspace: %w", fsdurable.ErrPublicationFailed, err)
	}
	defer os.RemoveAll(dir)
	if err = e.preflight(dir, filepath.Dir(output)); err != nil {
		return Result{}, err
	}
	tmp := filepath.Join(dir, "output."+o.Format)
	start, end, err := app.ExportLocal(ctx, e.config, input, tmp, req.Ranges[0], req)
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	// Synchronize completed bytes before exclusive linking, then persist the
	// output directory entry before reporting success.
	info, err := e.publish(ctx, tmp, output)
	if err != nil {
		if errors.Is(err, os.ErrExist) && !errors.Is(err, fsdurable.ErrPublicationUncertain) {
			return Result{}, &messageCause{message: ErrOutputExists.Error(), cause: errors.Join(ErrOutputExists, err)}
		}
		return Result{}, err
	}
	return Result{Path: output, SizeBytes: info.Size(), ActualStartMS: start, ActualEndMS: end}, nil
}
