package localmedia

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediapreflight"
)

func mediaPreflightResult(result mediapreflight.Result) PreflightResult {
	return PreflightResult{Version: result.Version, Valid: result.Valid, Source: result.Source,
		Request: ExportRequest(result.Request), RequestedMS: result.RequestedMS, OutputCount: result.OutputCount,
		TrackManifest: result.TrackManifest, Video: result.Video, Checks: result.Checks, Warnings: result.Warnings}
}

func mediaPreflightReport(result PreflightResult) mediapreflight.Result {
	return mediapreflight.Result{Version: result.Version, Valid: result.Valid, Source: result.Source,
		Request: edit.ExportRequest(result.Request), RequestedMS: result.RequestedMS, OutputCount: result.OutputCount,
		TrackManifest: result.TrackManifest, Video: result.Video, Checks: result.Checks, Warnings: result.Warnings}
}

func preflightGroups(groups []mediapreflight.CapabilityGroup) []PreflightCapabilityGroup {
	if groups == nil {
		return nil
	}
	result := make([]PreflightCapabilityGroup, len(groups))
	for i, group := range groups {
		result[i] = PreflightCapabilityGroup{Listing: group.Listing, Code: group.Code, Required: group.Required}
	}
	return result
}

func mediaPreflightGroups(groups []PreflightCapabilityGroup) []mediapreflight.CapabilityGroup {
	if groups == nil {
		return nil
	}
	result := make([]mediapreflight.CapabilityGroup, len(groups))
	for i, group := range groups {
		result[i] = mediapreflight.CapabilityGroup{Listing: group.Listing, Code: group.Code, Required: group.Required}
	}
	return result
}

func applyPreflightListing(group PreflightCapabilityGroup, available map[string]bool, deferred bool, result *PreflightResult) error {
	report := mediaPreflightReport(*result)
	err := mediapreflight.ApplyListing(mediapreflight.CapabilityGroup{Listing: group.Listing, Code: group.Code, Required: group.Required}, available, deferred, &report)
	*result = mediaPreflightResult(report)
	return mediaExportError(err)
}

type mediaPreflightBatchRuntime struct {
	config   Config
	Scope    *PreflightBatchScope
	query    PreflightQueryFunc
	snapshot *PreflightCapabilitySnapshot
}

func (runtime *mediaPreflightBatchRuntime) Unchanged(ctx context.Context) error {
	return runtime.Scope.Unchanged(ctx)
}

func (runtime *mediaPreflightBatchRuntime) Check(ctx context.Context, groups []mediapreflight.CapabilityGroup, result *mediapreflight.Result, wanted map[string]map[string]bool) error {
	if runtime.snapshot == nil {
		runtime.snapshot = &PreflightCapabilitySnapshot{Scope: runtime.Scope, Wanted: wanted, Listings: make(map[string]PreflightCapabilityListing)}
	}
	report := mediaPreflightResult(*result)
	err := runtime.snapshot.Check(ctx, runtime.config, preflightGroups(groups), &report, runtime.query)
	*result = mediaPreflightReport(report)
	return err
}
