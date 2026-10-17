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

const preflightCapabilityBytes = mediapreflight.CapabilityBytes

type PreflightProbeFunc func(context.Context, Config, string, bool) (mediainfo.Info, int64, error)
type PreflightQueryFunc func(context.Context, string, ...string) ([]byte, error)

// PreflightLocal inspects one local source and verifies the prerequisites that
// can be checked without decoding, rendering, staging subtitles or writing an
// output. A successful result is advisory: execution must inspect its input
// again and still verify the completed media and its publication.
func PreflightLocal(ctx context.Context, c Config, path string, request ExportRequest) (PreflightResult, error) {
	return PreflightLocalWithQueries(ctx, c, path, request, preflightProbe, QueryPreflightCapabilities)
}

func PreflightLocalWithQueries(ctx context.Context, c Config, path string, request ExportRequest, inspect PreflightProbeFunc, query PreflightQueryFunc) (PreflightResult, error) {
	if err := ctx.Err(); err != nil {
		return PreflightResult{}, err
	}
	if path == "" {
		return PreflightResult{}, &mediaerror.Problem{Code: "invalid_argument", Message: "input path is required"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return PreflightResult{}, err
	}
	before, err := PreflightFileIdentity(absolute)
	if err != nil {
		return PreflightResult{}, err
	}
	if !before.Mode().IsRegular() {
		return PreflightResult{}, &mediaerror.Problem{Code: "invalid_argument", Message: "input must be a readable local file"}
	}
	// Always start a fresh inspection session, even if the caller holds a
	// session from an earlier operation. Its metadata must not certify this one.
	ctx, session := BeginLocalExport(ctx, absolute)
	defer session.Close()
	p, duration, err := inspect(ctx, c, absolute, false)
	if err != nil {
		return PreflightResult{}, &mediaerror.InspectionFailure{Cause: err}
	}
	if err := PreflightSourceUnchanged(absolute, before); err != nil {
		return PreflightResult{}, err
	}
	result, groups, err := PrepareLocalPreflight(c, absolute, request, probeInfo(p), duration)
	if err != nil {
		return PreflightResult{}, err
	}
	if err := CheckPreflightCapabilities(ctx, c, groups, &result, query); err != nil {
		return PreflightResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PreflightResult{}, err
	}
	if err := PreflightSourceUnchanged(absolute, before); err != nil {
		return PreflightResult{}, err
	}
	FinishLocalPreflight(&result)
	return result, nil
}

// PrepareLocalPreflight owns every report's mutable fields. Both single and
// batch checks use the same request validation, stream selection and geometry.
func PrepareLocalPreflight(c Config, path string, request ExportRequest, p probeInfo, duration int64) (PreflightResult, []PreflightCapabilityGroup, error) {
	result, groups, err := mediapreflight.Prepare(coremodel.EditLimits(c), path, edit.ExportRequest(request), mediainfo.Info(p), duration)
	return mediaPreflightResult(result), preflightGroups(groups), mediaExportError(err)
}

func FinishLocalPreflight(result *PreflightResult) {
	report := mediaPreflightReport(*result)
	mediapreflight.Finish(&report)
	*result = mediaPreflightResult(report)
}

func PreflightSourceUnchanged(path string, before os.FileInfo) error {
	after, err := PreflightFileIdentity(path)
	if err != nil {
		return err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return &mediaerror.Problem{Code: "source_changed", Message: "The source has changed; inspect it again before exporting"}
	}
	return nil
}

// File.Stat records identity from the opened handle on Windows too; path-only
// metadata can otherwise defer obtaining identity until after a replacement.
func PreflightFileIdentity(path string) (os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

// These source-only guards mirror the existing export preparation. Copy keeps
// coded geometry and never silently acquires an encode, resize or HDR step.

type PreflightCapabilityGroup struct {
	Listing, Code string
	Required      []string
}

func QueryPreflightCapabilities(ctx context.Context, path string, args ...string) ([]byte, error) {
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

func CheckPreflightCapabilities(ctx context.Context, c Config, groups []PreflightCapabilityGroup, result *PreflightResult, query PreflightQueryFunc) error {
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
	before, err := PreflightFileIdentity(tool)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return &mediaerror.Problem{Code: "tool_unavailable", Message: "The configured media tool must be a regular executable file"}
	}
	for _, group := range groups {
		if len(group.Required) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		data, queryErr := query(queryCtx, tool, "-hide_banner", "-"+group.Listing)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		var executable *exec.Error
		var pathError *os.PathError
		if errors.As(queryErr, &executable) || errors.As(queryErr, &pathError) && pathError.Op == "fork/exec" {
			return queryErr
		}
		available, parsed := parsePreflightCapabilityList(group.Listing, data)
		if err := applyPreflightListing(group, available, queryErr != nil || len(data) > preflightCapabilityBytes || !parsed, result); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	after, err := PreflightFileIdentity(tool)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return &mediaerror.Problem{Code: "tool_unavailable", Message: "The configured media tool changed while checking capabilities; inspect it again"}
	}
	return nil
}

// Capability names are read only from complete, recognized FFmpeg tables.
// A substring in descriptive text or an unrecognized response proves nothing.
func parsePreflightCapabilityList(kind string, data []byte) (map[string]bool, bool) {
	return mediapreflight.ParseCapabilityList(kind, data)
}

func preflightProbe(ctx context.Context, c Config, path string, remote bool) (mediainfo.Info, int64, error) {
	p, d, err := probe(ctx, c, path, remote)
	return mediainfo.Info(p), d, err
}
