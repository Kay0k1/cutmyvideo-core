package localmedia

import (
	"context"
	"errors"
	"github.com/Kay0k1/cutmyvideo-core/internal/coremodel"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediapreflight"
)

// PreflightLocalBatch shares only this invocation's fresh source inspection
// and capability tables. Execution and the next request inspect again. Reports
// remain independent, and every selected request is checked before admission.
func PreflightLocalBatch(ctx context.Context, c Config, path string, requests []ExportRequest) ([]PreflightResult, int, error) {
	return PreflightLocalBatchWithQueries(ctx, c, path, requests, preflightProbe, QueryPreflightCapabilities)
}

func PreflightLocalBatchWithQueries(ctx context.Context, c Config, path string, requests []ExportRequest, inspect PreflightProbeFunc, query PreflightQueryFunc) ([]PreflightResult, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if len(requests) == 0 {
		return []PreflightResult{}, -1, nil
	}
	if len(requests) > 128 {
		return nil, 0, &mediaerror.Problem{Code: "invalid_argument", Message: "preflight must contain at most 128 requests"}
	}
	if path == "" {
		return nil, 0, &mediaerror.Problem{Code: "invalid_argument", Message: "input path is required"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, 0, err
	}
	before, err := PreflightFileIdentity(absolute)
	if err != nil {
		return nil, 0, err
	}
	if !before.Mode().IsRegular() {
		return nil, 0, &mediaerror.Problem{Code: "invalid_argument", Message: "input must be a readable local file"}
	}
	probeTool, err := PinPreflightExecutable(c.FFprobe)
	if err != nil {
		return nil, 0, &mediaerror.InspectionFailure{Cause: err}
	}
	scope := PreflightBatchScope{Path: absolute, Source: before, Probe: probeTool}
	// Never certify this operation with a caller's earlier inspection session.
	ctx, session := BeginLocalExport(ctx, absolute)
	defer session.Close()
	if err := scope.Unchanged(ctx); err != nil {
		return nil, 0, err
	}
	p, duration, err := inspect(ctx, c, absolute, false)
	if err != nil {
		return nil, 0, &mediaerror.InspectionFailure{Cause: err}
	}
	if err := scope.Unchanged(ctx); err != nil {
		return nil, 0, err
	}

	neutralRequests := make([]edit.ExportRequest, len(requests))
	for i, request := range requests {
		neutralRequests[i] = edit.ExportRequest(request)
	}
	reports, failed, err := mediapreflight.Batch(ctx, coremodel.EditLimits(c), absolute, neutralRequests, mediainfo.Info(p), duration, &mediaPreflightBatchRuntime{config: c, Scope: &scope, query: query})
	if err != nil {
		return nil, failed, mediaExportError(err)
	}
	results := make([]PreflightResult, len(reports))
	for i, report := range reports {
		results[i] = mediaPreflightResult(report)
	}
	return results, failed, nil
}

type PreflightExecutable struct {
	Configured, Path string
	File             os.FileInfo
}

func PinPreflightExecutable(configured string) (PreflightExecutable, error) {
	path, err := exec.LookPath(configured)
	if err != nil {
		return PreflightExecutable{}, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return PreflightExecutable{}, err
	}
	file, err := PreflightFileIdentity(path)
	if err != nil {
		return PreflightExecutable{}, err
	}
	if !file.Mode().IsRegular() {
		return PreflightExecutable{}, &mediaerror.Problem{Code: "tool_unavailable", Message: "The configured media tool must be a regular executable file"}
	}
	return PreflightExecutable{Configured: configured, Path: path, File: file}, nil
}

func (tool PreflightExecutable) Unchanged() error {
	after, err := PinPreflightExecutable(tool.Configured)
	if err != nil || after.Path != tool.Path || !os.SameFile(tool.File, after.File) || tool.File.Size() != after.File.Size() || !tool.File.ModTime().Equal(after.File.ModTime()) {
		return &mediaerror.Problem{Code: "tool_unavailable", Message: "The configured media tool changed while checking capabilities; inspect it again"}
	}
	return nil
}

type PreflightBatchScope struct {
	Path    string
	Source  os.FileInfo
	Probe   PreflightExecutable
	Encoder *PreflightExecutable
}

func (scope *PreflightBatchScope) Unchanged(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := PreflightSourceUnchanged(scope.Path, scope.Source); err != nil {
		return err
	}
	if err := scope.Probe.Unchanged(); err != nil {
		return err
	}
	if scope.Encoder != nil {
		return scope.Encoder.Unchanged()
	}
	return nil
}

type PreflightCapabilityListing struct {
	Available map[string]bool
	Deferred  bool
}

type PreflightCapabilitySnapshot struct {
	Scope    *PreflightBatchScope
	Wanted   map[string]map[string]bool
	Listings map[string]PreflightCapabilityListing
}

func (snapshot *PreflightCapabilitySnapshot) Check(ctx context.Context, c Config, groups []PreflightCapabilityGroup, result *PreflightResult, query PreflightQueryFunc) error {
	if err := snapshot.Scope.Unchanged(ctx); err != nil {
		return err
	}
	if snapshot.Scope.Encoder == nil {
		tool, err := PinPreflightExecutable(c.FFmpeg)
		if err != nil {
			return err
		}
		snapshot.Scope.Encoder = &tool
	}
	for _, group := range groups {
		if len(group.Required) == 0 {
			continue
		}
		if err := snapshot.Scope.Unchanged(ctx); err != nil {
			return err
		}
		listing, cached := snapshot.Listings[group.Listing]
		if !cached {
			queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			data, queryErr := query(queryCtx, snapshot.Scope.Encoder.Path, "-hide_banner", "-"+group.Listing)
			cancel()
			if err := snapshot.Scope.Unchanged(ctx); err != nil {
				return err
			}
			var executable *exec.Error
			var pathError *os.PathError
			if errors.As(queryErr, &executable) || errors.As(queryErr, &pathError) && pathError.Op == "fork/exec" {
				return queryErr
			}
			available, parsed := parsePreflightCapabilityList(group.Listing, data)
			listing.Deferred = queryErr != nil || len(data) > preflightCapabilityBytes || !parsed
			if !listing.Deferred {
				listing.Available = make(map[string]bool, len(snapshot.Wanted[group.Listing]))
				for name := range snapshot.Wanted[group.Listing] {
					// Keys come from validated requirements, never substrings of
					// the large native response retained by the temporary parser.
					listing.Available[name] = available[name]
				}
			}
			snapshot.Listings[group.Listing] = listing
		}
		if err := applyPreflightListing(group, listing.Available, listing.Deferred, result); err != nil {
			return err
		}
		if err := snapshot.Scope.Unchanged(ctx); err != nil {
			return err
		}
	}
	return snapshot.Scope.Unchanged(ctx)
}
