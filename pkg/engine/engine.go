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

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

// Config controls external media tools and resource limits. Zero fields use
// the documented defaults; invalid values fail Inspect and Export.
type Config struct {
	FFmpegPath, FFprobePath string
	MaxOutputBytes          int64
	// EncodeProfile accepts "fast" (default) or "compact"; both preserve
	// selected display resolution and source frame cadence. Fast spends fewer
	// CPU cycles and yields larger files. FFmpegThreads defaults to 2 and is
	// limited to 32.
	EncodeProfile string
	FFmpegThreads int
	// Timeout bounds include inspection; an earlier caller deadline wins.
	// Zero uses 2 minutes for inspection and 30 minutes for a whole export.
	InspectTimeout, ExportTimeout time.Duration
}

// Engine is immutable after New and safe to share between callers. Each
// operation has its own deadline and temporary output workspace.
type Engine struct {
	config         localmedia.Config
	configErr      error
	inspectTimeout time.Duration
	exportTimeout  time.Duration
	preflight      func(string, string) error
	publish        func(context.Context, string, string) (fs.FileInfo, error)
}

// Range identifies an interval on the original source timeline in milliseconds.
type Range struct{ StartMS, EndMS int64 }

// Options selects mp4/mp3/mkv, best/1080p/720p and accurate/copy respectively.
// MKV requires copy mode; MP3 requires accurate mode.
// Empty fields infer the format from the output suffix and use best/accurate.
type Options struct {
	Format  string `json:"format,omitempty"`
	Quality string `json:"quality,omitempty"`
	CutMode string `json:"cut_mode,omitempty"`
}

// SourceInfo describes a finite local media file. Width and Height are the
// selected video's coded dimensions; DisplayWidth and DisplayHeight account
// for supported sample aspect ratio and display rotation. Unsupported display
// geometry omits display dimensions; audio files omit all dimensions.
type SourceInfo struct {
	DurationMS    int64 `json:"duration_ms"`
	Width         int   `json:"width,omitempty"`
	Height        int   `json:"height,omitempty"`
	DisplayWidth  int   `json:"display_width,omitempty"`
	DisplayHeight int   `json:"display_height,omitempty"`
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
	e := &Engine{config: localmedia.Config{FFmpeg: c.FFmpegPath, FFprobe: c.FFprobePath, FFmpegProfile: c.EncodeProfile, FFmpegThreads: c.FFmpegThreads, MaxOutputBytes: c.MaxOutputBytes, MaxRanges: 128, MaxRangeMS: 24 * 3600000, MaxJobMS: 24 * 3600000}, inspectTimeout: c.InspectTimeout, exportTimeout: c.ExportTimeout}
	e.preflight, e.publish = fsdurable.Preflight, fsdurable.Publish
	if c.MaxOutputBytes < 0 || c.FFmpegThreads < 1 || c.FFmpegThreads > 32 || (c.EncodeProfile != "fast" && c.EncodeProfile != "compact") || c.InspectTimeout <= 0 || c.ExportTimeout <= 0 {
		e.configErr = errors.New("invalid engine limits, profile, threads, or timeout")
	}
	return e
}

// Inspect reads duration and dimensions with FFprobe within InspectTimeout.
// Only regular local files and supported finite media formats are accepted.
func (e *Engine) Inspect(ctx context.Context, path string) (SourceInfo, error) {
	result, err := e.InspectWithTracks(ctx, path)
	return result.SourceInfo, err
}

// InspectWithTracks adds stream inspection to the unchanged 1.x SourceInfo.
func (e *Engine) InspectWithTracks(ctx context.Context, path string) (result AdvancedSourceInfo, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return AdvancedSourceInfo{}, invalidArgument(e.configErr)
	}
	if path == "" {
		return AdvancedSourceInfo{}, invalidArgument(errors.New("input path is required"))
	}
	if err := ctx.Err(); err != nil {
		return AdvancedSourceInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.inspectTimeout)
	defer cancel()
	// An existing relative filename beginning with '-' is not a tool option.
	// Resolving it before ffprobe also avoids dependence on its working directory.
	path, err = filepath.Abs(path)
	if err != nil {
		return AdvancedSourceInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return AdvancedSourceInfo{}, &messageCause{message: "input must be a readable local file", cause: err}
	}
	if !info.Mode().IsRegular() {
		return AdvancedSourceInfo{}, invalidArgument(errors.New("input must be a readable local file"))
	}
	s, err := localmedia.InspectLocal(ctx, e.config, path)
	return AdvancedSourceInfo{SourceInfo: SourceInfo{DurationMS: s.DurationMS, Width: s.Width, Height: s.Height, DisplayWidth: s.DisplayWidth, DisplayHeight: s.DisplayHeight}, Tracks: s.Tracks}, err
}

// Export validates the interval, writes and verifies a temporary media file,
// then publishes it exclusively at output. The output directory must exist.
// Its containing directory hierarchy must already be persistent; Export syncs
// the output entry, not arbitrary directories recently created by the caller.
// Cancellation and failures before publication remove temporary files. A
// publication_uncertain error means the final output may exist; inspect it
// before retrying. Publication requires hard links and directory sync support.
func (e *Engine) Export(ctx context.Context, input, output string, r Range, o Options) (result Result, err error) {
	advanced, exportErr := e.ExportWithOptions(ctx, input, output, r, AdvancedOptions{Options: o})
	return advanced.Result, exportErr
}

// ExportWithOptions adds explicit audio and subtitle selection while retaining
// the existing Export and Options contracts.
func (e *Engine) ExportWithOptions(ctx context.Context, input, output string, r Range, o AdvancedOptions) (result AdvancedResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return AdvancedResult{}, invalidArgument(e.configErr)
	}
	if input == "" || output == "" {
		return AdvancedResult{}, invalidArgument(errors.New("input and output paths are required"))
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	input, err = filepath.Abs(input)
	if err != nil {
		return AdvancedResult{}, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return AdvancedResult{}, err
	}
	ctx, session := localmedia.BeginLocalExport(ctx, input)
	defer session.Close()
	s, err := e.InspectWithTracks(ctx, input)
	if err != nil {
		return AdvancedResult{}, err
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
	req := localmedia.ExportRequest{Ranges: []localmedia.Range{{StartMS: r.StartMS, EndMS: r.EndMS}}, Format: o.Format, Quality: o.Quality, CutMode: o.CutMode, AudioTrack: o.AudioTrack, NoAudio: o.NoAudio, SubtitleMode: o.SubtitleMode, Crop: o.Crop}
	if err = req.Validate(e.config, localmedia.Source{DurationMS: s.DurationMS, Path: input}); err != nil {
		return AdvancedResult{}, invalidArgument(err)
	}
	tracks, err := localmedia.SelectExportTracks(s.Tracks, req)
	if err != nil {
		return AdvancedResult{}, err
	}
	if _, err = os.Lstat(output); err == nil {
		return AdvancedResult{}, &messageCause{message: ErrOutputExists.Error(), cause: errors.Join(ErrOutputExists, os.ErrExist)}
	} else if !errors.Is(err, os.ErrNotExist) {
		return AdvancedResult{}, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(output), ".cutmy-")
	if err != nil {
		return AdvancedResult{}, fmt.Errorf("%w: create output workspace: %w", fsdurable.ErrPublicationFailed, err)
	}
	defer os.RemoveAll(dir)
	session.SetWorkspace(dir)
	if err = e.preflight(dir, filepath.Dir(output)); err != nil {
		return AdvancedResult{}, err
	}
	tmp := filepath.Join(dir, "output."+o.Format)
	start, end, err := localmedia.ExportLocal(ctx, e.config, input, tmp, req.Ranges[0], req)
	if err != nil {
		return AdvancedResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return AdvancedResult{}, err
	}
	// Synchronize completed bytes before exclusive linking, then persist the
	// output directory entry before reporting success.
	info, err := e.publish(ctx, tmp, output)
	if err != nil {
		if errors.Is(err, os.ErrExist) && !errors.Is(err, fsdurable.ErrPublicationUncertain) {
			return AdvancedResult{}, &messageCause{message: ErrOutputExists.Error(), cause: errors.Join(ErrOutputExists, err)}
		}
		return AdvancedResult{}, err
	}
	return AdvancedResult{Result: Result{Path: output, SizeBytes: info.Size(), ActualStartMS: start, ActualEndMS: end}, TrackManifest: tracks, Segments: []Segment{{SourceStartMS: start, SourceEndMS: end, OutputEndMS: end - start}}}, nil
}
