package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type Track = edit.Track
type TrackSelection = edit.TrackSelection
type Segment = edit.Segment
type Crop = edit.Crop
type AudioNormalize = edit.AudioNormalize
type Canvas = edit.Canvas
type AudioReplace = edit.AudioReplace
type Speed = edit.Speed

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
	AudioTrack      *int            `json:"audio_track,omitempty"`
	NoAudio         bool            `json:"no_audio,omitempty"`
	SubtitleMode    string          `json:"subtitle_mode,omitempty"`
	Crop            *Crop           `json:"crop,omitempty"`
	AudioNormalize  *AudioNormalize `json:"audio_normalize,omitempty"`
	Canvas          *Canvas         `json:"canvas,omitempty"`
	AudioReplace    *AudioReplace   `json:"audio_replace,omitempty"`
	Speed           *Speed          `json:"speed,omitempty"`
	TargetSizeBytes int64           `json:"target_size_bytes,omitempty"`
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
	input, output, request, err := planRequest(plan, output)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	ctx, session := localmedia.BeginLocalExport(ctx, input)
	defer session.Close()
	source, err := e.InspectWithTracks(ctx, input)
	if err != nil {
		return result, err
	}
	if err = request.Validate(e.config, localmedia.Source{DurationMS: source.DurationMS, Path: input}); err != nil {
		return result, invalidArgument(err)
	}
	ctx, releaseReplacement, err := prepareEngineReplacement(ctx, request)
	if err != nil {
		return result, err
	}
	defer releaseReplacement()
	ranges := request.Ranges
	session.SetRanges(ranges)
	tracks, err := localmedia.SelectExportTracks(source.Tracks, request)
	if err != nil {
		return result, err
	}
	// Joined exports retain their existing workspace/publication error contract.
	// Preflight additionally inspects their proposed parent without writing it.
	parent, names, err := planOutputPaths(output, request, false)
	if err != nil {
		return result, err
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
	if request.OutputMode == "joined" {
		path := filepath.Join(dir, "output."+request.Format)
		segments, e := localmedia.ExportJoinedLocal(ctx, e.config, input, path, ranges, request)
		if e != nil {
			return result, e
		}
		duration := segments[len(segments)-1].OutputEndMS
		prepared = append(prepared, BatchOutput{Result: Result{Path: path, ActualEndMS: duration}, Segments: segments, TrackManifest: tracks})
	} else {
		var totalBytes int64
		type interval struct{ start, end int64 }
		type reusable struct {
			path       string
			info       os.FileInfo
			start, end int64
		}
		counts := make(map[interval]int)
		for _, r := range ranges {
			counts[interval{r.StartMS, r.EndMS}]++
		}
		completed := make(map[interval]reusable)
		for i, r := range ranges {
			path := filepath.Join(dir, fmt.Sprintf("output-%d.%s", i, request.Format))
			key := interval{r.StartMS, r.EndMS}
			var start, end int64
			var exportErr error
			reused := false
			if prior, ok := completed[key]; ok {
				reused, exportErr = localmedia.ReuseLocalExport(ctx, input, prior.path, path, prior.info, e.config.MaxOutputBytes)
				if reused {
					start, end = prior.start, prior.end
				}
			}
			if exportErr == nil && !reused {
				start, end, exportErr = localmedia.ExportLocal(ctx, e.config, input, path, r, request)
			}
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
			if err = localmedia.CheckAudioReplacement(ctx); err != nil {
				return result, err
			}
			info, publishErr := e.publish(ctx, path, names[i])
			if publishErr != nil {
				if errors.Is(publishErr, os.ErrExist) && !errors.Is(publishErr, fsdurable.ErrPublicationUncertain) {
					publishErr = errors.Join(ErrOutputExists, publishErr)
				}
				return result, publishErr
			}
			outputDuration, _ := edit.OutputDurationMS(end-start, request.Speed)
			result.Outputs = append(result.Outputs, BatchOutput{Result: Result{Path: names[i], SizeBytes: info.Size(), ActualStartMS: start, ActualEndMS: end}, Label: r.Label, Segments: []Segment{{Index: i, Label: r.Label, SourceStartMS: start, SourceEndMS: end, OutputEndMS: outputDuration}}, TrackManifest: tracks})
			if counts[key] > 1 {
				completed[key] = reusable{names[i], info, start, end}
			}
			_ = os.Remove(path)
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	for i, item := range prepared {
		if err = localmedia.CheckAudioReplacement(ctx); err != nil {
			return result, err
		}
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
