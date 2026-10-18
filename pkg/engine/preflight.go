package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type PreflightCheck = localmedia.PreflightCheck
type PreflightWarning = localmedia.PreflightWarning
type PreflightSource = localmedia.PreflightSource
type PreflightVideo = localmedia.PreflightVideo
type PreflightRequest = localmedia.ExportRequest
type PreflightRange = edit.Range

// PreflightResult contains known prerequisites and explicitly deferred checks.
// OutputPaths are proposed names only. No output or workspace is created and
// no filesystem publication capability or final storage capacity is promised.
type PreflightResult struct {
	Version       int                `json:"version"`
	Valid         bool               `json:"valid"`
	Source        PreflightSource    `json:"source"`
	Request       PreflightRequest   `json:"request"`
	RequestedMS   int64              `json:"requested_duration_ms"`
	OutputCount   int                `json:"output_count"`
	TrackManifest []TrackSelection   `json:"track_manifest"`
	Video         *PreflightVideo    `json:"video,omitempty"`
	Checks        []PreflightCheck   `json:"checks"`
	Warnings      []PreflightWarning `json:"warnings"`
	OutputPaths   []string           `json:"output_paths"`
}

// PreflightPlan checks a local plan without rendering, reserving or publishing
// media. It shares defaults, range validation and output naming with ExecutePlan.
// The configured inspection deadline includes all capability queries. A passed
// check is a snapshot; ExecutePlan rechecks sources and execution prerequisites.
func (e *Engine) PreflightPlan(ctx context.Context, plan Plan, output string) (result PreflightResult, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return result, invalidArgument(e.configErr)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	input, output, request, err := planRequest(plan, output)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.inspectTimeout)
	defer cancel()
	info, err := os.Stat(input)
	if err != nil {
		return result, &messageCause{message: "input must be a readable local file", cause: err}
	}
	if !info.Mode().IsRegular() {
		return result, invalidArgument(errors.New("input must be a readable local file"))
	}
	checked, err := localmedia.PreflightLocal(ctx, e.config, input, request)
	if err != nil {
		return result, err
	}
	_, names, err := planOutputPaths(output, checked.Request, true)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	checked.Checks = append(checked.Checks, localmedia.PreflightCheck{Name: "output_paths", Status: "passed"})
	return PreflightResult{Version: checked.Version, Valid: checked.Valid, Source: checked.Source,
		Request: checked.Request, RequestedMS: checked.RequestedMS, OutputCount: checked.OutputCount,
		TrackManifest: checked.TrackManifest, Video: checked.Video, Checks: checked.Checks,
		Warnings: checked.Warnings, OutputPaths: names}, nil
}

func planRequest(plan Plan, output string) (input, absoluteOutput string, request localmedia.ExportRequest, err error) {
	if plan.Version != 1 {
		err = invalidArgument(errors.New("plan version must be 1"))
		return
	}
	if plan.Input == "" || output == "" {
		err = invalidArgument(errors.New("plan input and output are required"))
		return
	}
	input, err = filepath.Abs(plan.Input)
	if err != nil {
		return
	}
	absoluteOutput, err = filepath.Abs(output)
	if err != nil {
		return
	}
	o := plan.Options
	if o.AudioReplace != nil && o.AudioReplace.SourceID != "" {
		err = invalidArgument(errors.New("local engine audio_replace requires input, not source_id"))
		return
	}
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
			o.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(absoluteOutput)), ".")
		} else {
			o.Format = "mp4"
		}
	}
	ranges := make([]localmedia.Range, len(plan.Ranges))
	for i, r := range plan.Ranges {
		ranges[i] = localmedia.Range{StartMS: r.StartMS, EndMS: r.EndMS, Label: r.Label}
	}
	request = localmedia.ExportRequest{Ranges: ranges, Format: o.Format, Quality: o.Quality, CutMode: o.CutMode,
		OutputMode: mode, NoAudio: o.NoAudio, SubtitleMode: o.SubtitleMode}
	request.AudioNormalize, request.Canvas, request.AudioReplace = edit.CloneAudioNormalize(o.AudioNormalize), edit.CloneCanvas(o.Canvas), edit.CloneAudioReplace(o.AudioReplace)
	request.Speed, request.TargetSizeBytes = edit.CloneSpeed(o.Speed), o.TargetSizeBytes
	if o.AudioTrack != nil {
		index := *o.AudioTrack
		request.AudioTrack = &index
	}
	if o.Crop != nil {
		crop := *o.Crop
		request.Crop = &crop
	}
	return
}

// This inspects existing directory entries without creating a probe or file.
// Actual exclusive publication still detects changes after these observations.
func planOutputPaths(output string, request localmedia.ExportRequest, inspectJoinedParent bool) (parent string, names []string, err error) {
	parent = filepath.Dir(output)
	if request.OutputMode == "joined" {
		names = []string{output}
	} else {
		parent = output
		for i, r := range request.Ranges {
			names = append(names, filepath.Join(output, fmt.Sprintf("%02d-%s.%s", i+1, outputLabel(r.Label), request.Format)))
		}
	}
	if request.OutputMode != "joined" || inspectJoinedParent {
		info, e := os.Stat(parent)
		if e != nil {
			return parent, nil, e
		}
		if !info.IsDir() {
			return parent, nil, invalidArgument(errors.New("output parent must be an existing directory"))
		}
	}
	for _, name := range names {
		if _, e := os.Lstat(name); e == nil {
			return parent, nil, &messageCause{message: ErrOutputExists.Error(), cause: errors.Join(ErrOutputExists, os.ErrExist)}
		} else if !errors.Is(e, os.ErrNotExist) {
			return parent, nil, e
		}
	}
	return parent, names, nil
}
