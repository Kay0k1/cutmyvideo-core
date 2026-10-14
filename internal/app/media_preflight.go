package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const preflightCapabilityBytes = 1 << 20

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
	source := Source{Path: absolute, DurationMS: duration, Tracks: p.tracks()}
	if video := p.selectedVideo(); video != nil {
		source.Width, source.Height = video.Width, video.Height
		source.DisplayWidth, source.DisplayHeight, _ = videoDisplayGeometry(video)
	}
	// Validate normalizes labels and defaults. Keep that mutation private to
	// this report rather than changing the caller's reusable request.
	request.Ranges = append([]Range(nil), request.Ranges...)
	if request.AudioTrack != nil {
		value := *request.AudioTrack
		request.AudioTrack = &value
	}
	if request.Crop != nil {
		value := *request.Crop
		request.Crop = &value
	}
	if err := request.Validate(c, source); err != nil {
		return PreflightResult{}, &sourceProblem{"invalid_argument", err.Error()}
	}
	selection := mediaSelection{Video: p.selectedVideo(), Audio: p.selectedAudio()}
	if request.NoAudio {
		selection.Audio = nil
	} else if request.AudioTrack != nil {
		selection.Audio = nil
		for i := range p.Streams {
			stream := &p.Streams[i]
			if stream.CodecType == "audio" && stream.Index == *request.AudioTrack {
				selection.Audio = stream
				break
			}
		}
		if selection.Audio == nil {
			return PreflightResult{}, &sourceProblem{"audio_track_unavailable", "The selected audio stream index is not available; inspect the source and choose an audio index"}
		}
	}
	tracks, err := trackManifest(p, selection, request)
	if err != nil {
		return PreflightResult{}, err
	}
	video, err := validatePreflightSelection(selection, request)
	if err != nil {
		return PreflightResult{}, err
	}
	result := preflightRequestResult(source, request)
	result.TrackManifest, result.Video = tracks, video
	result.Checks = append(result.Checks, PreflightCheck{Name: "source", Status: "passed"}, PreflightCheck{Name: "tracks", Status: "passed"})
	if video != nil {
		result.Checks = append(result.Checks, PreflightCheck{Name: "video_geometry", Status: "passed"})
	}
	if err := checkPreflightCapabilities(ctx, c, preflightRequiredCapabilities(selection, request, tracks, video), &result, query); err != nil {
		return PreflightResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PreflightResult{}, err
	}
	if err := preflightSourceUnchanged(absolute, before); err != nil {
		return PreflightResult{}, err
	}
	if request.CutMode == "copy" {
		addPreflightDeferred(&result, "copy_boundaries", "copy_boundaries_deferred", "Copy boundaries are determined from source packets and nearby keyframes during export")
		addPreflightDeferred(&result, "copy_container", "copy_container_unverified", "The configured muxer verifies whether the selected codecs can be copied into the requested container during export")
	}
	addPreflightDeferred(&result, "media_decode", "media_decode_deferred", "Source payload decoding and completed output verification occur during export")
	addPreflightDeferred(&result, "output_size", "output_size_deferred", "The completed output must satisfy its size limit during export")
	addPreflightDeferred(&result, "storage", "storage_admission_deferred", "Available storage and execution admission are checked when an export is started")
	addPreflightDeferred(&result, "publication", "publication_deferred", "Output paths, filesystem capabilities and exclusive publication are checked during execution")
	return result, nil
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
	if request.Format == "mp3" {
		if selection.Audio == nil {
			return nil, errNoAudio
		}
		return nil, nil
	}
	if selection.Video == nil {
		return nil, errUnsupportedStream
	}
	if request.CutMode == "copy" {
		if request.Quality != "best" {
			_, height, err := videoDisplayGeometry(selection.Video)
			if err != nil {
				return nil, err
			}
			limit := 1080
			if request.Quality == "720p" {
				limit = 720
			}
			if height > limit {
				return nil, &sourceProblem{"copy_quality_unsupported", "Copy mode cannot reduce this video's resolution. Choose original quality or accurate mode"}
			}
		}
		return &PreflightVideo{Width: selection.Video.Width, Height: selection.Video.Height}, nil
	}
	_, hdr, err := videoCropRenderFilter(selection.Video, request.Quality, request.Crop)
	if err != nil {
		return nil, err
	}
	if _, err := videoEncoderTimeBase(selection.Video); err != nil {
		return nil, err
	}
	width, height, err := videoOutputGeometryWithCrop(selection.Video, request.Quality, false, request.Crop)
	return &PreflightVideo{Width: width, Height: height, HDRToSDR: hdr}, err
}

type preflightCapabilityGroup struct {
	listing, code string
	required      []string
}

func preflightRequiredCapabilities(selection mediaSelection, request ExportRequest, tracks []TrackSelection, video *PreflightVideo) []preflightCapabilityGroup {
	var encoders, filters, demuxers, bsfs []string
	muxer := request.Format
	if muxer == "mkv" {
		muxer = "matroska"
	}
	muxers := []string{muxer}
	joined := request.OutputMode == "joined"
	if request.CutMode != "copy" {
		if request.Format == "mp3" {
			encoders = append(encoders, "libmp3lame")
		} else {
			encoders = append(encoders, "libx264")
			filters = append(filters, "scale", "setsar")
			if request.Crop != nil {
				filters = append(filters, "crop")
			}
			if selection.Audio != nil {
				encoders = append(encoders, "aac")
			}
			if video != nil && video.HDRToSDR {
				filters = append(filters, "zscale", "format", "setparams", "tonemap")
				bsfs = append(bsfs, "h264_metadata")
			}
			if joined {
				filters = append(filters, "select")
			}
		}
		if joined && selection.Audio != nil {
			filters = append(filters, "aselect", "aresample")
		}
	}
	if joined {
		demuxers = append(demuxers, "concat")
		if request.CutMode == "copy" {
			if request.Format == "mkv" {
				demuxers = append(demuxers, "matroska")
			} else {
				demuxers = append(demuxers, "mov")
				// Joined MP4 copy always stages selected audio as Matroska;
				// the optional direct packet path is limited to MKV.
				if selection.Audio != nil {
					muxers = append(muxers, "matroska")
					demuxers = append(demuxers, "matroska")
				}
			}
		}
	}
	for _, track := range tracks {
		if !track.Included || track.Type != "subtitle" {
			continue
		}
		encoders = append(encoders, "subrip")
		muxers = append(muxers, "srt")
		demuxers = append(demuxers, "srt")
		if request.Format == "mp4" {
			encoders = append(encoders, "mov_text")
			demuxers = append(demuxers, "mov")
		} else {
			demuxers = append(demuxers, "matroska")
		}
		break
	}
	return []preflightCapabilityGroup{
		{"encoders", "encoder_unavailable", encoders},
		{"muxers", "muxer_unavailable", muxers},
		{"filters", "filter_unavailable", filters},
		{"demuxers", "demuxer_unavailable", demuxers},
		{"bsfs", "bitstream_filter_unavailable", bsfs},
	}
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
		if queryErr != nil || len(data) > preflightCapabilityBytes || !parsed {
			addPreflightDeferred(result, "tool_"+group.listing, "tool_capabilities_unverified", "The configured media tool's "+group.listing+" could not be verified without processing media")
			continue
		}
		for _, name := range group.required {
			if !available[name] {
				return &sourceProblem{group.code, "The configured media tool does not provide required " + group.listing + " capability " + name}
			}
		}
		result.Checks = append(result.Checks, PreflightCheck{Name: "tool_" + group.listing, Status: "passed"})
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
	if len(data) == 0 || len(data) > preflightCapabilityBytes {
		return nil, false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	header := strings.TrimSpace(lines[0])
	validHeader := kind == "encoders" && header == "Encoders:" || kind == "filters" && header == "Filters:" || kind == "bsfs" && header == "Bitstream filters:" || (kind == "muxers" || kind == "demuxers") && (header == "Formats:" || header == "File formats:" || kind == "muxers" && header == "Muxers:" || kind == "demuxers" && header == "Demuxers:")
	if !validHeader {
		return nil, false
	}
	entries := make(map[string]bool)
	rows := kind == "bsfs"
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !rows {
			if strings.Trim(line, "-") == "" {
				rows = true
			}
			continue
		}
		fields := strings.Fields(line)
		if kind == "bsfs" {
			if len(fields) != 1 || !preflightCapabilityName(fields[0]) {
				return nil, false
			}
			entries[fields[0]] = true
			continue
		}
		if len(fields) < 3 {
			return nil, false
		}
		flags := fields[0]
		switch kind {
		case "encoders":
			if len(flags) != 6 || !strings.ContainsRune("VAS", rune(flags[0])) || strings.Trim(flags[1:], ".FSXBD") != "" {
				return nil, false
			}
		case "filters":
			if len(flags) < 2 || len(flags) > 3 || strings.Trim(flags, ".TSC") != "" || len(fields) < 4 || !strings.Contains(fields[2], "->") {
				return nil, false
			}
		case "muxers", "demuxers":
			if len(flags) > 3 || strings.Trim(flags, "DEd.") != "" || !strings.ContainsAny(flags, "DE") {
				return nil, false
			}
		}
		for _, name := range strings.Split(fields[1], ",") {
			if !preflightCapabilityName(name) {
				return nil, false
			}
			if kind == "muxers" && !strings.Contains(flags, "E") || kind == "demuxers" && !strings.Contains(flags, "D") {
				continue
			}
			entries[name] = true
		}
	}
	return entries, rows && len(entries) > 0
}

func preflightCapabilityName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, ch := range name {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}
