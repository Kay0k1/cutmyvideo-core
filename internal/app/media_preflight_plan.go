package app

import (
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

func preflightGroups(groups []mediapreflight.CapabilityGroup) []preflightCapabilityGroup {
	if groups == nil {
		return nil
	}
	result := make([]preflightCapabilityGroup, len(groups))
	for i, group := range groups {
		result[i] = preflightCapabilityGroup{listing: group.Listing, code: group.Code, required: group.Required}
	}
	return result
}

func mediaPreflightGroups(groups []preflightCapabilityGroup) []mediapreflight.CapabilityGroup {
	if groups == nil {
		return nil
	}
	result := make([]mediapreflight.CapabilityGroup, len(groups))
	for i, group := range groups {
		result[i] = mediapreflight.CapabilityGroup{Listing: group.listing, Code: group.code, Required: group.required}
	}
	return result
}

func applyPreflightListing(group preflightCapabilityGroup, available map[string]bool, deferred bool, result *PreflightResult) error {
	report := mediaPreflightReport(*result)
	err := mediapreflight.ApplyListing(mediapreflight.CapabilityGroup{Listing: group.listing, Code: group.code, Required: group.required}, available, deferred, &report)
	*result = mediaPreflightResult(report)
	return mediaExportError(err)
}
