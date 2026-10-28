package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"os"
)

func preflightLocalBatch(ctx context.Context, c Config, path string, requests []ExportRequest) ([]PreflightResult, int, error) {
	result, failed, err := localmedia.PreflightLocalBatch(ctx, c, path, requests)
	return result, failed, localMediaError(err)
}
func preflightLocalBatchWithQueries(ctx context.Context, c Config, path string, requests []ExportRequest, inspect preflightProbeFunc, query preflightQueryFunc) ([]PreflightResult, int, error) {
	result, failed, err := localmedia.PreflightLocalBatchWithQueries(ctx, c, path, requests, localPreflightProbe(inspect), localmedia.PreflightQueryFunc(query))
	return result, failed, localMediaError(err)
}

type preflightExecutable struct {
	configured, path string
	file             os.FileInfo
}

func pinPreflightExecutable(configured string) (preflightExecutable, error) {
	tool, err := localmedia.PinPreflightExecutable(configured)
	return preflightExecutable{configured: tool.Configured, path: tool.Path, file: tool.File}, localMediaError(err)
}
func (tool preflightExecutable) backend() localmedia.PreflightExecutable {
	return localmedia.PreflightExecutable{Configured: tool.configured, Path: tool.path, File: tool.file}
}

type preflightBatchScope struct {
	path    string
	source  os.FileInfo
	probe   preflightExecutable
	encoder *preflightExecutable
}

func (scope *preflightBatchScope) backend() *localmedia.PreflightBatchScope {
	out := &localmedia.PreflightBatchScope{Path: scope.path, Source: scope.source, Probe: scope.probe.backend()}
	if scope.encoder != nil {
		tool := scope.encoder.backend()
		out.Encoder = &tool
	}
	return out
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
	backend := localmedia.PreflightCapabilitySnapshot{Scope: snapshot.scope.backend(), Wanted: snapshot.wanted, Listings: make(map[string]localmedia.PreflightCapabilityListing, len(snapshot.listings))}
	for name, listing := range snapshot.listings {
		backend.Listings[name] = localmedia.PreflightCapabilityListing{Available: listing.available, Deferred: listing.deferred}
	}
	err := backend.Check(ctx, c, localPreflightGroups(groups), result, localmedia.PreflightQueryFunc(query))
	if backend.Scope.Encoder != nil {
		tool := backend.Scope.Encoder
		snapshot.scope.encoder = &preflightExecutable{configured: tool.Configured, path: tool.Path, file: tool.File}
	}
	for name, listing := range backend.Listings {
		snapshot.listings[name] = preflightCapabilityListing{available: listing.Available, deferred: listing.Deferred}
	}
	return localMediaError(err)
}
