package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediapreflight"
	"os"
)

const preflightCapabilityBytes = mediapreflight.CapabilityBytes

type preflightProbeFunc func(context.Context, Config, string, bool) (probeInfo, int64, error)
type preflightQueryFunc func(context.Context, string, ...string) ([]byte, error)

func localPreflightProbe(inspect preflightProbeFunc) localmedia.PreflightProbeFunc {
	return func(ctx context.Context, c Config, path string, remote bool) (mediainfo.Info, int64, error) {
		p, d, err := inspect(ctx, c, path, remote)
		return mediainfo.Info(p), d, err
	}
}
func PreflightLocal(ctx context.Context, c Config, path string, request ExportRequest) (PreflightResult, error) {
	result, err := localmedia.PreflightLocal(ctx, c, path, request)
	return result, localMediaError(err)
}
func preflightLocalWithQueries(ctx context.Context, c Config, path string, request ExportRequest, inspect preflightProbeFunc, query preflightQueryFunc) (PreflightResult, error) {
	result, err := localmedia.PreflightLocalWithQueries(ctx, c, path, request, localPreflightProbe(inspect), localmedia.PreflightQueryFunc(query))
	return result, localMediaError(err)
}
func preflightSourceUnchanged(path string, before os.FileInfo) error {
	return localMediaError(localmedia.PreflightSourceUnchanged(path, before))
}
func preflightFileIdentity(path string) (os.FileInfo, error) {
	return localmedia.PreflightFileIdentity(path)
}
func queryPreflightCapabilities(ctx context.Context, path string, args ...string) ([]byte, error) {
	data, err := localmedia.QueryPreflightCapabilities(ctx, path, args...)
	return data, localMediaError(err)
}

type preflightCapabilityGroup struct {
	listing, code string
	required      []string
}

func localPreflightGroups(groups []preflightCapabilityGroup) []localmedia.PreflightCapabilityGroup {
	if groups == nil {
		return nil
	}
	out := make([]localmedia.PreflightCapabilityGroup, len(groups))
	for i, g := range groups {
		out[i] = localmedia.PreflightCapabilityGroup{Listing: g.listing, Code: g.code, Required: g.required}
	}
	return out
}
func checkPreflightCapabilities(ctx context.Context, c Config, groups []preflightCapabilityGroup, result *PreflightResult, query preflightQueryFunc) error {
	return localMediaError(localmedia.CheckPreflightCapabilities(ctx, c, localPreflightGroups(groups), result, localmedia.PreflightQueryFunc(query)))
}

func prepareLocalPreflight(c Config, path string, request ExportRequest, p probeInfo, duration int64) (PreflightResult, []preflightCapabilityGroup, error) {
	result, groups, err := mediapreflight.Prepare(editLimits(c), path, edit.ExportRequest(request), mediainfo.Info(p), duration)
	return mediaPreflightResult(result), preflightGroups(groups), mediaExportError(err)
}

func finishLocalPreflight(result *PreflightResult) {
	report := mediaPreflightReport(*result)
	mediapreflight.Finish(&report)
	*result = mediaPreflightResult(report)
}

func validatePreflightSelection(selection mediaSelection, request ExportRequest) (*PreflightVideo, error) {
	video, err := mediapreflight.ValidateSelection(mediaExportSelection(selection), edit.ExportRequest(request))
	return video, mediaExportError(err)
}

func preflightRequiredCapabilities(selection mediaSelection, request ExportRequest, tracks []TrackSelection, video *PreflightVideo) []preflightCapabilityGroup {
	return preflightGroups(mediapreflight.RequiredCapabilities(mediaExportSelection(selection), edit.ExportRequest(request), tracks, video))
}

func parsePreflightCapabilityList(kind string, data []byte) (map[string]bool, bool) {
	return mediapreflight.ParseCapabilityList(kind, data)
}
