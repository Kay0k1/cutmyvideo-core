package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

type Track = app.Track
type TrackSelection = app.TrackSelection
type Segment = app.ArtifactSegment
type Crop = app.Crop

type AdvancedSourceInfo struct {
	SourceInfo
	Tracks []Track `json:"tracks"`
}
type AdvancedResult struct {
	Result
	TrackManifest []TrackSelection `json:"track_manifest"`
	Segments      []Segment        `json:"segments"`
}

// AdvancedOptions extends the 1.x three-field Options without breaking its
// positional struct literals. AudioTrack is an absolute inspected stream index.
type AdvancedOptions struct {
	Options
	AudioTrack   *int   `json:"audio_track,omitempty"`
	NoAudio      bool   `json:"no_audio,omitempty"`
	SubtitleMode string `json:"subtitle_mode,omitempty"`
	Crop         *Crop  `json:"crop,omitempty"`
}
type NamedRange struct {
	Label   string `json:"label"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
}

// Plan is a reusable, versioned batch request. Input is a local media path.
// ExecutePlan preserves range order; output is a directory for separate files
// and an explicit filename for a joined result. Empty OutputMode is separate.
type Plan struct {
	Version    int             `json:"version"`
	Input      string          `json:"input"`
	Ranges     []NamedRange    `json:"ranges"`
	Options    AdvancedOptions `json:"options"`
	OutputMode string          `json:"output_mode,omitempty"`
}
type BatchOutput struct {
	Result
	Label         string           `json:"label,omitempty"`
	Segments      []Segment        `json:"segments"`
	TrackManifest []TrackSelection `json:"track_manifest"`
}
type BatchResult struct {
	Outputs []BatchOutput `json:"outputs"`
}

func outputLabel(label string) string {
	var result strings.Builder
	for _, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			result.WriteRune(r)
		} else if result.Len() > 0 {
			result.WriteByte('_')
		}
		if result.Len() >= 80 {
			break
		}
	}
	value := strings.Trim(result.String(), "_")
	if value == "" {
		return "clip"
	}
	return value
}

// ExecutePlan validates all ranges and output names before processing. Existing
// files are preserved. If exclusive publication fails after another output was
// published, the returned Outputs identifies the completed files.
func (e *Engine) ExecutePlan(ctx context.Context, plan Plan, output string) (result BatchResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	if plan.Version != 1 {
		return result, invalidArgument(errors.New("plan version must be 1"))
	}
	if plan.Input == "" || output == "" {
		return result, invalidArgument(errors.New("plan input and output are required"))
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	input, err := filepath.Abs(plan.Input)
	if err != nil {
		return result, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return result, err
	}
	ctx, session := app.BeginLocalExport(ctx, input)
	defer session.Close()
	source, err := e.InspectWithTracks(ctx, input)
	if err != nil {
		return result, err
	}
	o := plan.Options
	if o.Quality == "" {
		o.Quality = "best"
	}
	if o.CutMode == "" {
		o.CutMode = "accurate"
	}
	mode := plan.OutputMode
	if mode == "" {
		mode = "separate"
	}
	if o.Format == "" {
		if mode == "joined" {
			o.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(output)), ".")
		} else {
			o.Format = "mp4"
		}
	}
	ranges := make([]app.Range, len(plan.Ranges))
	for i, r := range plan.Ranges {
		ranges[i] = app.Range{StartMS: r.StartMS, EndMS: r.EndMS, Label: r.Label}
	}
	request := app.ExportRequest{Ranges: ranges, Format: o.Format, Quality: o.Quality, CutMode: o.CutMode, OutputMode: mode, AudioTrack: o.AudioTrack, NoAudio: o.NoAudio, SubtitleMode: o.SubtitleMode, Crop: o.Crop}
	if err = request.Validate(e.config, app.Source{DurationMS: source.DurationMS, Path: input}); err != nil {
		return result, invalidArgument(err)
	}
	tracks, err := app.SelectExportTracks(source.Tracks, request)
	if err != nil {
		return result, err
	}
	parent := filepath.Dir(output)
	var names []string
	if mode == "joined" {
		names = []string{output}
	} else {
		info, e := os.Stat(output)
		if e != nil {
			return result, e
		}
		if !info.IsDir() {
			return result, invalidArgument(errors.New("separate output must be an existing directory"))
		}
		parent = output
		for i, r := range ranges {
			names = append(names, filepath.Join(output, fmt.Sprintf("%02d-%s.%s", i+1, outputLabel(r.Label), o.Format)))
		}
	}
	for _, name := range names {
		if _, e := os.Lstat(name); e == nil {
			return result, &messageCause{message: ErrOutputExists.Error(), cause: errors.Join(ErrOutputExists, os.ErrExist)}
		} else if !errors.Is(e, os.ErrNotExist) {
			return result, e
		}
	}
	dir, err := os.MkdirTemp(parent, ".cutmy-batch-")
	if err != nil {
		return result, fmt.Errorf("%w: create output workspace: %w", fsdurable.ErrPublicationFailed, err)
	}
	defer os.RemoveAll(dir)
	session.SetWorkspace(dir)
	if err = e.preflight(dir, parent); err != nil {
		return result, err
	}
	prepared := make([]BatchOutput, 0, len(names))
	if mode == "joined" {
		path := filepath.Join(dir, "output."+o.Format)
		segments, e := app.ExportJoinedLocal(ctx, e.config, input, path, ranges, request)
		if e != nil {
			return result, e
		}
		duration := segments[len(segments)-1].OutputEndMS
		prepared = append(prepared, BatchOutput{Result: Result{Path: path, ActualEndMS: duration}, Segments: segments, TrackManifest: tracks})
	} else {
		var totalBytes int64
		for i, r := range ranges {
			path := filepath.Join(dir, fmt.Sprintf("output-%d.%s", i, o.Format))
			start, end, exportErr := app.ExportLocal(ctx, e.config, input, path, r, request)
			if exportErr != nil {
				return result, exportErr
			}
			stat, statErr := os.Stat(path)
			if statErr != nil {
				return result, statErr
			}
			if stat.Size() > e.config.MaxOutputBytes-totalBytes {
				return result, &Error{Code: CodeOutputLimit, Err: errors.New("total batch output exceeds the configured byte limit")}
			}
			totalBytes += stat.Size()
			info, publishErr := e.publish(ctx, path, names[i])
			if publishErr != nil {
				if errors.Is(publishErr, os.ErrExist) && !errors.Is(publishErr, fsdurable.ErrPublicationUncertain) {
					publishErr = errors.Join(ErrOutputExists, publishErr)
				}
				return result, publishErr
			}
			result.Outputs = append(result.Outputs, BatchOutput{Result: Result{Path: names[i], SizeBytes: info.Size(), ActualStartMS: start, ActualEndMS: end}, Label: r.Label, Segments: []Segment{{Index: i, Label: r.Label, SourceStartMS: start, SourceEndMS: end, OutputEndMS: end - start}}, TrackManifest: tracks})
			_ = os.Remove(path)
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	for i, item := range prepared {
		info, e := e.publish(ctx, item.Path, names[i])
		if e != nil {
			if errors.Is(e, os.ErrExist) && !errors.Is(e, fsdurable.ErrPublicationUncertain) {
				e = errors.Join(ErrOutputExists, e)
			}
			return result, e
		}
		item.Path, item.SizeBytes = names[i], info.Size()
		result.Outputs = append(result.Outputs, item)
	}
	return result, nil
}
