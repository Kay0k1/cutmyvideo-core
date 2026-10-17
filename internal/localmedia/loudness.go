package localmedia

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/toolrun"
)

type LoudnessOptions = edit.LoudnessOptions
type LoudnessResult = edit.LoudnessResult

func AnalyzeLoudness(ctx context.Context, c Config, path string, options LoudnessOptions) (LoudnessResult, error) {
	var result LoudnessResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if path == "" {
		return result, &mediaerror.Problem{Code: "invalid_argument", Message: "loudness input path is required"}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	before, err := PreflightFileIdentity(path)
	if err != nil {
		return result, err
	}
	if !before.Mode().IsRegular() {
		return result, &mediaerror.Problem{Code: "invalid_argument", Message: "loudness input must be a readable local file"}
	}
	ctx, session := BeginLocalExport(ctx, path)
	defer session.Close()
	p, duration, err := probe(ctx, c, path, false)
	if err != nil {
		return result, err
	}
	end := options.EndMS
	if end == 0 {
		end = duration
	}
	if options.StartMS < 0 || end <= options.StartMS || end > duration || end-options.StartMS > 24*3600000 || c.MaxJobMS > 0 && end-options.StartMS > c.MaxJobMS {
		return result, &mediaerror.Problem{Code: "invalid_argument", Message: "loudness window must be a positive bounded interval within the source"}
	}
	selected := p.selectedAudio()
	if options.AudioTrack != nil {
		selected = nil
		for i := range p.Streams {
			if p.Streams[i].CodecType == "audio" && p.Streams[i].Index == *options.AudioTrack {
				selected = &p.Streams[i]
				break
			}
		}
	}
	if selected == nil {
		return result, &mediaerror.Problem{Code: "audio_track_unavailable", Message: "selected loudness audio track is unavailable"}
	}
	if selected.Channels < 1 || selected.Channels > 8 {
		return result, &mediaerror.Problem{Code: "invalid_argument", Message: "loudness analysis requires between 1 and 8 channels"}
	}
	if err := PreflightSourceUnchanged(path, before); err != nil {
		return result, err
	}
	args := []string{"-hide_banner", "-v", "info", "-nostdin", "-nostats", "-xerror", "-max_alloc", "268435456", "-filter_threads", "1", "-progress", "pipe:1", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-ss", seconds(options.StartMS), "-i", session.Path, "-t", seconds(end-options.StartMS), "-map", "0:"+strconv.Itoa(selected.Index), "-vn", "-sn", "-af", "atrim=duration="+seconds(end-options.StartMS)+",asetpts=PTS-STARTPTS,loudnorm=I=-16:TP=-1:LRA=11:print_format=json", "-f", "null", "-")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	diagnostics := &processResponseBuffer{limit: 64 << 10, cancel: cancel}
	var decoded int64
	progress := NewProgressWriter(end-options.StartMS, func(ms int64) { decoded = ms })
	r := toolrun.RunOutputWithDiagnostics(runCtx, c.FFmpeg, args, progress, diagnostics)
	observeProcess(ctx, c.FFmpeg, r.Elapsed, r.ProcessErr)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if diagnostics.overflow {
		return result, mediaerror.ErrProcessResponseTooLarge
	}
	if r.ProcessErr != nil {
		return result, &mediaerror.ProcessFailure{Cause: r.ProcessErr, Category: r.Category}
	}
	if decoded <= 0 {
		return result, errors.New("loudness analysis decoded no audio samples")
	}
	measurement, err := mediaexport.ParseLoudnessReport(diagnostics.buffer.Bytes())
	if err != nil {
		return result, err
	}
	if err := PreflightSourceUnchanged(path, before); err != nil {
		return result, err
	}
	return LoudnessResult{Version: 1, StartMS: options.StartMS, EndMS: end, AnalyzedEndMS: options.StartMS + decoded, AudioTrack: selected.Index, IntegratedLUFS: measurement.IntegratedLUFS, TruePeakDBTP: measurement.TruePeakDBTP, LoudnessRangeLU: measurement.LoudnessRangeLU}, nil
}
