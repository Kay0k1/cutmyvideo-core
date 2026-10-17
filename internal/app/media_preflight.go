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

const preflightCapabilityBytes = mediapreflight.CapabilityBytes

type preflightProbeFunc func(context.Context, Config, string, bool) (probeInfo, int64, error)
type preflightQueryFunc func(context.Context, string, ...string) ([]byte, error)

// PreflightLocal inspects one local source and verifies the prerequisites that
// can be checked without decoding, rendering, staging subtitles or writing an
// output. A successful result is advisory: execution must inspect its input
// again and still verify the completed media and its publication.
func PreflightLocal(ctx context.Context, c Config, path string, request ExportRequest) (PreflightResult, error) {
	return preflightLocalWithQueries(ctx, c, path, request, probe, queryPreflightCapabilities)
}

func preflightLocalWithQueries(ctx context.Context, c Config, path string, request ExportRequest, inspect preflightProbeFunc, query preflightQueryFunc) (PreflightResult, error) {
	if err := ctx.Err(); err != nil {
		return PreflightResult{}, err
	}
	if path == "" {
		return PreflightResult{}, &sourceProblem{"invalid_argument", "input path is required"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return PreflightResult{}, err
	}
	before, err := preflightFileIdentity(absolute)
	if err != nil {
		return PreflightResult{}, err
	}
	if !before.Mode().IsRegular() {
		return PreflightResult{}, &sourceProblem{"invalid_argument", "input must be a readable local file"}
	}
	// Always start a fresh inspection session, even if the caller holds a
	// session from an earlier operation. Its metadata must not certify this one.
	ctx, session := BeginLocalExport(ctx, absolute)
	defer session.Close()
	p, duration, err := inspect(ctx, c, absolute, false)
	if err != nil {
		return PreflightResult{}, &localInspectionFailure{cause: err}
	}
	if err := preflightSourceUnchanged(absolute, before); err != nil {
		return PreflightResult{}, err
	}
	result, groups, err := prepareLocalPreflight(c, absolute, request, p, duration)
	if err != nil {
		return PreflightResult{}, err
	}
	if err := checkPreflightCapabilities(ctx, c, groups, &result, query); err != nil {
		return PreflightResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PreflightResult{}, err
	}
	if err := preflightSourceUnchanged(absolute, before); err != nil {
		return PreflightResult{}, err
	}
	finishLocalPreflight(&result)
	return result, nil
}

// prepareLocalPreflight owns every report's mutable fields. Both single and
// batch checks use the same request validation, stream selection and geometry.
func prepareLocalPreflight(c Config, path string, request ExportRequest, p probeInfo, duration int64) (PreflightResult, []preflightCapabilityGroup, error) {
	result, groups, err := mediapreflight.Prepare(editLimits(c), path, edit.ExportRequest(request), mediainfo.Info(p), duration)
	return mediaPreflightResult(result), preflightGroups(groups), mediaExportError(err)
}

func finishLocalPreflight(result *PreflightResult) {
	report := mediaPreflightReport(*result)
	mediapreflight.Finish(&report)
	*result = mediaPreflightResult(report)
}

func preflightSourceUnchanged(path string, before os.FileInfo) error {
	after, err := preflightFileIdentity(path)
	if err != nil {
		return err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return &sourceProblem{"source_changed", "The source has changed; inspect it again before exporting"}
	}
	return nil
}

// File.Stat records identity from the opened handle on Windows too; path-only
// metadata can otherwise defer obtaining identity until after a replacement.
func preflightFileIdentity(path string) (os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

// These source-only guards mirror the existing export preparation. Copy keeps
// coded geometry and never silently acquires an encode, resize or HDR step.
func validatePreflightSelection(selection mediaSelection, request ExportRequest) (*PreflightVideo, error) {
	video, err := mediapreflight.ValidateSelection(mediaExportSelection(selection), edit.ExportRequest(request))
	return video, mediaExportError(err)
}

type preflightCapabilityGroup struct {
	listing, code string
	required      []string
}

func preflightRequiredCapabilities(selection mediaSelection, request ExportRequest, tracks []TrackSelection, video *PreflightVideo) []preflightCapabilityGroup {
	return preflightGroups(mediapreflight.RequiredCapabilities(mediaExportSelection(selection), edit.ExportRequest(request), tracks, video))
}

func queryPreflightCapabilities(ctx context.Context, path string, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &processResponseBuffer{limit: preflightCapabilityBytes, cancel: cancel}
	err := runCommandOutput(runCtx, path, args, stdout)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.overflow {
		return nil, errProcessResponseTooLarge
	}
	return stdout.buffer.Bytes(), err
}

func checkPreflightCapabilities(ctx context.Context, c Config, groups []preflightCapabilityGroup, result *PreflightResult, query preflightQueryFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tool, err := exec.LookPath(c.FFmpeg)
	if err != nil {
		return err
	}
	tool, err = filepath.Abs(tool)
	if err != nil {
		return err
	}
	before, err := preflightFileIdentity(tool)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return &sourceProblem{"tool_unavailable", "The configured media tool must be a regular executable file"}
	}
	for _, group := range groups {
		if len(group.required) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		data, queryErr := query(queryCtx, tool, "-hide_banner", "-"+group.listing)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		var executable *exec.Error
		var pathError *os.PathError
		if errors.As(queryErr, &executable) || errors.As(queryErr, &pathError) && pathError.Op == "fork/exec" {
			return queryErr
		}
		available, parsed := parsePreflightCapabilityList(group.listing, data)
		if err := applyPreflightListing(group, available, queryErr != nil || len(data) > preflightCapabilityBytes || !parsed, result); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	after, err := preflightFileIdentity(tool)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return &sourceProblem{"tool_unavailable", "The configured media tool changed while checking capabilities; inspect it again"}
	}
	return nil
}

// Capability names are read only from complete, recognized FFmpeg tables.
// A substring in descriptive text or an unrecognized response proves nothing.
func parsePreflightCapabilityList(kind string, data []byte) (map[string]bool, bool) {
	return mediapreflight.ParseCapabilityList(kind, data)
}
