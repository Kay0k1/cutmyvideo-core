package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

type CollectionPlan = app.CollectionPlan
type CollectionClip = app.CollectionClip
type CollectionOptions = app.CollectionOptions
type CollectionExecution = app.CollectionExecution
type CollectionRange = app.Range
type CollectionFieldError = app.CollectionFieldError

type CollectionClipPreflight struct {
	ID            string             `json:"id"`
	Label         string             `json:"label,omitempty"`
	RequestedMS   int64              `json:"requested_duration_ms"`
	Request       PreflightRequest   `json:"request"`
	TrackManifest []TrackSelection   `json:"track_manifest"`
	Video         *PreflightVideo    `json:"video,omitempty"`
	Checks        []PreflightCheck   `json:"checks"`
	Warnings      []PreflightWarning `json:"warnings"`
	OutputPath    string             `json:"output_path"`
}

type CollectionPreflightResult struct {
	Version         int                       `json:"version"`
	Valid           bool                      `json:"valid"`
	Rendition       string                    `json:"rendition"`
	Source          PreflightSource           `json:"source"`
	SelectedClipIDs []string                  `json:"selected_clip_ids"`
	Clips           []CollectionClipPreflight `json:"clips"`
}

type CollectionOutput struct {
	Result
	ID              string            `json:"id"`
	Label           string            `json:"label,omitempty"`
	RequestedRanges []CollectionRange `json:"requested_ranges"`
	Segments        []Segment         `json:"segments"`
	TrackManifest   []TrackSelection  `json:"track_manifest"`
}

// Outputs lists only successfully published clips. On a later error callers
// retain those files; retry selected remaining IDs or use a new directory.
type CollectionResult struct {
	Version   int                `json:"version"`
	Rendition string             `json:"rendition"`
	Outputs   []CollectionOutput `json:"outputs"`
}

// PreflightCollection checks all selected edits and output collisions without
// creating files or rendering. Whole-plan IDs/structure and selected media
// settings are checked; capability checks reuse the shared media preflight.
func (e *Engine) PreflightCollection(ctx context.Context, plan CollectionPlan, output string, execution CollectionExecution) (result CollectionPreflightResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	input, dir, err := collectionPaths(plan, output)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.inspectTimeout)
	defer cancel()
	ctx, session := app.BeginLocalExport(ctx, input)
	defer session.Close()
	result, _, err = e.prepareCollection(ctx, plan, input, dir, execution)
	return result, err
}

// ExecuteCollection joins each selected clip's intervals in caller order and
// publishes ID-named outputs exclusively. All selected media prerequisites and
// output names are checked before encoding. One deadline and byte budget cover
// the complete operation, including inspection and publication.
func (e *Engine) ExecuteCollection(ctx context.Context, plan CollectionPlan, output string, execution CollectionExecution) (result CollectionResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	input, dir, err := collectionPaths(plan, output)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	ctx, session := app.BeginLocalExport(ctx, input)
	defer session.Close()
	inspectCtx, stopInspect := context.WithTimeout(ctx, e.inspectTimeout)
	checked, clips, err := e.prepareCollection(inspectCtx, plan, input, dir, execution)
	stopInspect()
	if err != nil {
		return result, err
	}
	result = CollectionResult{Version: 1, Rendition: checked.Rendition, Outputs: []CollectionOutput{}}
	ctx = app.CollectionRenderContext(ctx, result.Rendition)
	var ranges []app.Range
	for _, clip := range clips {
		ranges = append(ranges, clip.Request.Ranges...)
	}
	session.SetRanges(ranges)
	workspace, err := os.MkdirTemp(dir, ".cutmy-collection-")
	if err != nil {
		return result, fmt.Errorf("%w: create collection workspace: %w", fsdurable.ErrPublicationFailed, err)
	}
	defer os.RemoveAll(workspace)
	session.SetWorkspace(workspace)
	if err = e.preflight(workspace, dir); err != nil {
		return result, err
	}
	var bytes int64
	for i, clip := range clips {
		if err = ctx.Err(); err != nil {
			return result, collectionClipError(clip.ID, "export", err)
		}
		config := e.config
		config.MaxOutputBytes -= bytes
		if config.MaxOutputBytes <= 0 {
			return result, collectionClipError(clip.ID, "export", &Error{Code: CodeOutputLimit, Err: errors.New("selected collection exceeds configured output byte limit")})
		}
		path := filepath.Join(workspace, fmt.Sprintf("clip-%d.%s", i, clip.Request.Format))
		segments, renderErr := app.ExportJoinedLocal(ctx, config, input, path, clip.Request.Ranges, clip.Request)
		if renderErr != nil {
			return result, collectionClipError(clip.ID, "export", renderErr)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return result, collectionClipError(clip.ID, "export", statErr)
		}
		if info.Size() > config.MaxOutputBytes {
			return result, collectionClipError(clip.ID, "export", &Error{Code: CodeOutputLimit, Err: errors.New("selected collection exceeds configured output byte limit")})
		}
		name := checked.Clips[i].OutputPath
		published, publishErr := e.publish(ctx, path, name)
		if publishErr != nil {
			if errors.Is(publishErr, os.ErrExist) && !errors.Is(publishErr, fsdurable.ErrPublicationUncertain) {
				publishErr = errors.Join(ErrOutputExists, publishErr)
			}
			return result, collectionClipError(clip.ID, "output_path", publishErr)
		}
		bytes += published.Size()
		duration := segments[len(segments)-1].OutputEndMS
		result.Outputs = append(result.Outputs, CollectionOutput{Result: Result{Path: name, SizeBytes: published.Size(), ActualEndMS: duration}, ID: clip.ID, Label: clip.Label, RequestedRanges: clip.Request.Ranges, Segments: segments, TrackManifest: checked.Clips[i].TrackManifest})
		_ = os.Remove(path)
	}
	return result, nil
}

func collectionPaths(plan CollectionPlan, output string) (input, dir string, err error) {
	if plan.Input == "" {
		return "", "", invalidArgument(&CollectionFieldError{Field: "input", Err: errors.New("local collection input is required")})
	}
	if output == "" {
		return "", "", invalidArgument(&CollectionFieldError{Field: "output", Err: errors.New("existing output directory is required")})
	}
	input, err = filepath.Abs(plan.Input)
	if err != nil {
		return
	}
	dir, err = filepath.Abs(output)
	if err != nil {
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", invalidArgument(errors.New("collection output must be an existing directory"))
	}
	return input, dir, nil
}

func collectionClipError(id, field string, err error) error {
	return &CollectionFieldError{ClipID: id, Field: field, Err: err}
}

func (e *Engine) prepareCollection(ctx context.Context, plan CollectionPlan, input, dir string, execution CollectionExecution) (result CollectionPreflightResult, clips []app.PreparedCollectionClip, err error) {
	source, err := e.InspectWithTracks(ctx, input)
	if err != nil {
		return result, nil, err
	}
	clips, err = app.NormalizeCollection(plan, execution, e.config, app.Source{Path: input, DurationMS: source.DurationMS, Tracks: source.Tracks})
	if err != nil {
		return result, nil, err
	}
	rendition, _ := app.NormalizeCollectionRendition(execution.Rendition)
	result = CollectionPreflightResult{Version: 1, Valid: true, Rendition: rendition, SelectedClipIDs: []string{}, Clips: []CollectionClipPreflight{}, Source: PreflightSource{DurationMS: source.DurationMS, Width: source.Width, Height: source.Height, DisplayWidth: source.DisplayWidth, DisplayHeight: source.DisplayHeight}}
	names := make([]string, len(clips))
	for i, clip := range clips {
		names[i] = filepath.Join(dir, app.CollectionOutputFilename(clip, rendition))
		if _, err = os.Lstat(names[i]); err == nil {
			return CollectionPreflightResult{}, nil, collectionClipError(clip.ID, "output_path", &messageCause{message: ErrOutputExists.Error(), cause: errors.Join(ErrOutputExists, os.ErrExist)})
		}
		if !errors.Is(err, os.ErrNotExist) {
			return CollectionPreflightResult{}, nil, collectionClipError(clip.ID, "output_path", err)
		}
	}
	// A request fingerprint omits ranges: those are already fully validated.
	// Identical settings share capability probes, including the source snapshot.
	cache := make(map[string]app.PreflightResult)
	for i, clip := range clips {
		fingerprint := clip.Request
		fingerprint.Ranges = nil
		key, _ := json.Marshal(fingerprint)
		checked, ok := cache[string(key)]
		if !ok {
			checked, err = app.PreflightLocal(ctx, e.config, input, clip.Request)
			if err != nil {
				return CollectionPreflightResult{}, nil, collectionClipError(clip.ID, collectionMediaErrorField(err), err)
			}
			cache[string(key)] = checked
		}
		checks := append([]PreflightCheck(nil), checked.Checks...)
		checks = append(checks, PreflightCheck{Name: "output_paths", Status: "passed"})
		result.SelectedClipIDs = append(result.SelectedClipIDs, clip.ID)
		result.Clips = append(result.Clips, CollectionClipPreflight{ID: clip.ID, Label: clip.Label, RequestedMS: clip.RequestedMS, Request: clip.Request, TrackManifest: checked.TrackManifest, Video: checked.Video, Checks: checks, Warnings: checked.Warnings, OutputPath: names[i]})
	}
	// Reusing this operation's original inspection detects source changes across
	// distinct settings checks and before the first output is encoded.
	_, err = e.InspectWithTracks(ctx, input)
	if err != nil {
		return CollectionPreflightResult{}, nil, err
	}
	return result, clips, nil
}

func collectionMediaErrorField(err error) string {
	switch CodeOf(err) {
	case CodeAudioTrackUnavailable:
		return "options.audio_track"
	case CodeSubtitleUnavailable, CodeSubtitleUnsupported:
		return "options.subtitle_mode"
	case CodeCopyQualityUnsupported:
		return "options.quality"
	case ErrorCode("crop_invalid"):
		return "options.crop"
	default:
		return "options"
	}
}
