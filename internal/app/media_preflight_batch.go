package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediapreflight"
)

// preflightLocalBatch shares only this invocation's fresh source inspection
// and capability tables. Execution and the next request inspect again. Reports
// remain independent, and every selected request is checked before admission.
func preflightLocalBatch(ctx context.Context, c Config, path string, requests []ExportRequest) ([]PreflightResult, int, error) {
	return preflightLocalBatchWithQueries(ctx, c, path, requests, probe, queryPreflightCapabilities)
}

func preflightLocalBatchWithQueries(ctx context.Context, c Config, path string, requests []ExportRequest, inspect preflightProbeFunc, query preflightQueryFunc) ([]PreflightResult, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if len(requests) == 0 {
		return []PreflightResult{}, -1, nil
	}
	if len(requests) > 128 {
		return nil, 0, &sourceProblem{"invalid_argument", "preflight must contain at most 128 requests"}
	}
	if path == "" {
		return nil, 0, &sourceProblem{"invalid_argument", "input path is required"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, 0, err
	}
	before, err := preflightFileIdentity(absolute)
	if err != nil {
		return nil, 0, err
	}
	if !before.Mode().IsRegular() {
		return nil, 0, &sourceProblem{"invalid_argument", "input must be a readable local file"}
	}
	probeTool, err := pinPreflightExecutable(c.FFprobe)
	if err != nil {
		return nil, 0, &localInspectionFailure{cause: err}
	}
	scope := preflightBatchScope{path: absolute, source: before, probe: probeTool}
	// Never certify this operation with a caller's earlier inspection session.
	ctx, session := BeginLocalExport(ctx, absolute)
	defer session.Close()
	if err := scope.unchanged(ctx); err != nil {
		return nil, 0, err
	}
	p, duration, err := inspect(ctx, c, absolute, false)
	if err != nil {
		return nil, 0, &localInspectionFailure{cause: err}
	}
	if err := scope.unchanged(ctx); err != nil {
		return nil, 0, err
	}

	neutralRequests := make([]edit.ExportRequest, len(requests))
	for i, request := range requests {
		neutralRequests[i] = edit.ExportRequest(request)
	}
	reports, failed, err := mediapreflight.Batch(ctx, editLimits(c), absolute, neutralRequests, mediainfo.Info(p), duration, &mediaPreflightBatchRuntime{config: c, scope: &scope, query: query})
	if err != nil {
		return nil, failed, mediaExportError(err)
	}
	results := make([]PreflightResult, len(reports))
	for i, report := range reports {
		results[i] = mediaPreflightResult(report)
	}
	return results, failed, nil
}

type preflightExecutable struct {
	configured, path string
	file             os.FileInfo
}

func pinPreflightExecutable(configured string) (preflightExecutable, error) {
	path, err := exec.LookPath(configured)
	if err != nil {
		return preflightExecutable{}, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return preflightExecutable{}, err
	}
	file, err := preflightFileIdentity(path)
	if err != nil {
		return preflightExecutable{}, err
	}
	if !file.Mode().IsRegular() {
		return preflightExecutable{}, &sourceProblem{"tool_unavailable", "The configured media tool must be a regular executable file"}
	}
	return preflightExecutable{configured: configured, path: path, file: file}, nil
}

func (tool preflightExecutable) unchanged() error {
	after, err := pinPreflightExecutable(tool.configured)
	if err != nil || after.path != tool.path || !os.SameFile(tool.file, after.file) || tool.file.Size() != after.file.Size() || !tool.file.ModTime().Equal(after.file.ModTime()) {
		return &sourceProblem{"tool_unavailable", "The configured media tool changed while checking capabilities; inspect it again"}
	}
	return nil
}

type preflightBatchScope struct {
	path    string
	source  os.FileInfo
	probe   preflightExecutable
	encoder *preflightExecutable
}

func (scope *preflightBatchScope) unchanged(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := preflightSourceUnchanged(scope.path, scope.source); err != nil {
		return err
	}
	if err := scope.probe.unchanged(); err != nil {
		return err
	}
	if scope.encoder != nil {
		return scope.encoder.unchanged()
	}
	return nil
}

type preflightCapabilityListing struct {
	available map[string]bool
	deferred  bool
}

type preflightCapabilitySnapshot struct {
	scope    *preflightBatchScope
	wanted   map[string]map[string]bool
	listings map[string]preflightCapabilityListing
}

func (snapshot *preflightCapabilitySnapshot) check(ctx context.Context, c Config, groups []preflightCapabilityGroup, result *PreflightResult, query preflightQueryFunc) error {
	if err := snapshot.scope.unchanged(ctx); err != nil {
		return err
	}
	if snapshot.scope.encoder == nil {
		tool, err := pinPreflightExecutable(c.FFmpeg)
		if err != nil {
			return err
		}
		snapshot.scope.encoder = &tool
	}
	for _, group := range groups {
		if len(group.required) == 0 {
			continue
		}
		if err := snapshot.scope.unchanged(ctx); err != nil {
			return err
		}
		listing, cached := snapshot.listings[group.listing]
		if !cached {
			queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			data, queryErr := query(queryCtx, snapshot.scope.encoder.path, "-hide_banner", "-"+group.listing)
			cancel()
			if err := snapshot.scope.unchanged(ctx); err != nil {
				return err
			}
			var executable *exec.Error
			var pathError *os.PathError
			if errors.As(queryErr, &executable) || errors.As(queryErr, &pathError) && pathError.Op == "fork/exec" {
				return queryErr
			}
			available, parsed := parsePreflightCapabilityList(group.listing, data)
			listing.deferred = queryErr != nil || len(data) > preflightCapabilityBytes || !parsed
			if !listing.deferred {
				listing.available = make(map[string]bool, len(snapshot.wanted[group.listing]))
				for name := range snapshot.wanted[group.listing] {
					// Keys come from validated requirements, never substrings of
					// the large native response retained by the temporary parser.
					listing.available[name] = available[name]
				}
			}
			snapshot.listings[group.listing] = listing
		}
		if err := applyPreflightListing(group, listing.available, listing.deferred, result); err != nil {
			return err
		}
		if err := snapshot.scope.unchanged(ctx); err != nil {
			return err
		}
	}
	return snapshot.scope.unchanged(ctx)
}
