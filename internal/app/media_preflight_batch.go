package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

	// Compute the relevant-name union without retaining raw tool output or all
	// parsed names. Delay each request's error until its turn so an earlier
	// missing capability still precedes a later invalid range or track choice.
	type prepared struct {
		result PreflightResult
		groups []preflightCapabilityGroup
		err    error
	}
	preparedRequests := make([]prepared, len(requests))
	wanted := make(map[string]map[string]bool)
	for i, request := range requests {
		if err := scope.unchanged(ctx); err != nil {
			return nil, i, err
		}
		item := &preparedRequests[i]
		item.result, item.groups, item.err = prepareLocalPreflight(c, absolute, request, p, duration)
		for _, group := range item.groups {
			if wanted[group.listing] == nil {
				wanted[group.listing] = make(map[string]bool)
			}
			for _, name := range group.required {
				wanted[group.listing][name] = true
			}
		}
		if err := scope.unchanged(ctx); err != nil {
			return nil, i, err
		}
	}
	snapshot := preflightCapabilitySnapshot{scope: &scope, wanted: wanted, listings: make(map[string]preflightCapabilityListing)}
	results := make([]PreflightResult, len(requests))
	for i := range preparedRequests {
		if err := scope.unchanged(ctx); err != nil {
			return nil, i, err
		}
		item := &preparedRequests[i]
		if item.err != nil {
			return nil, i, item.err
		}
		if err := snapshot.check(ctx, c, item.groups, &item.result, query); err != nil {
			return nil, i, err
		}
		if err := scope.unchanged(ctx); err != nil {
			return nil, i, err
		}
		finishLocalPreflight(&item.result)
		results[i] = item.result
	}
	if err := scope.unchanged(ctx); err != nil {
		return nil, len(requests) - 1, err
	}
	return results, -1, nil
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
		if listing.deferred {
			addPreflightDeferred(result, "tool_"+group.listing, "tool_capabilities_unverified", "The configured media tool's "+group.listing+" could not be verified without processing media")
		} else {
			for _, name := range group.required {
				if !listing.available[name] {
					return &sourceProblem{group.code, "The configured media tool does not provide required " + group.listing + " capability " + name}
				}
			}
			result.Checks = append(result.Checks, PreflightCheck{Name: "tool_" + group.listing, Status: "passed"})
		}
		if err := snapshot.scope.unchanged(ctx); err != nil {
			return err
		}
	}
	return snapshot.scope.unchanged(ctx)
}
