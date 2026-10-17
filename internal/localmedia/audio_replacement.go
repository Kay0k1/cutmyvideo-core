package localmedia

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type replacementKey struct{}
type replacementScope struct {
	path string
	file os.FileInfo
}

// WithAudioReplacement binds a previously authorized local file to one
// operation. Service callers retain their own owner checks, durable job pins
// and reader leases. The path is private context state and never serialized.
func WithAudioReplacement(ctx context.Context, path string) (context.Context, func(), error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ctx, func() {}, err
	}
	file, err := PreflightFileIdentity(absolute)
	if err != nil {
		return ctx, func() {}, err
	}
	if !file.Mode().IsRegular() {
		return ctx, func() {}, &mediaerror.Problem{Code: "invalid_argument", Message: "replacement input must be a readable local file"}
	}
	return context.WithValue(ctx, replacementKey{}, &replacementScope{path: absolute, file: file}), func() {}, nil
}

func CheckAudioReplacement(ctx context.Context) error {
	scope, _ := ctx.Value(replacementKey{}).(*replacementScope)
	if scope == nil {
		return nil
	}
	return PreflightSourceUnchanged(scope.path, scope.file)
}

// PrepareAudioReplacement permits a local Input or reuses a service-bound
// context. A SourceID alone can never be interpreted as a filesystem path.
func PrepareAudioReplacement(ctx context.Context, request ExportRequest) (context.Context, func(), error) {
	if request.AudioReplace == nil {
		return ctx, func() {}, nil
	}
	if scope, _ := ctx.Value(replacementKey{}).(*replacementScope); scope != nil {
		if request.AudioReplace.Input != "" {
			p, err := filepath.Abs(request.AudioReplace.Input)
			if err != nil || p != scope.path {
				return ctx, func() {}, &mediaerror.Problem{Code: "invalid_argument", Message: "replacement input does not match its pinned operation"}
			}
		}
		return ctx, func() {}, CheckAudioReplacement(ctx)
	}
	if request.AudioReplace.Input == "" || request.AudioReplace.SourceID != "" {
		return ctx, func() {}, &mediaerror.Problem{Code: "invalid_argument", Message: "replacement source must be authorized and bound to a local file"}
	}
	return WithAudioReplacement(ctx, request.AudioReplace.Input)
}

func (runtime mediaExportRuntime) Replacement(ctx context.Context, options edit.AudioReplace) (mediaexport.Input, *mediainfo.Stream, error) {
	scope, _ := ctx.Value(replacementKey{}).(*replacementScope)
	if scope == nil {
		return mediaexport.Input{}, nil, &mediaerror.Problem{Code: "invalid_argument", Message: "replacement input has no authorized local file binding"}
	}
	if err := CheckAudioReplacement(ctx); err != nil {
		return mediaexport.Input{}, nil, err
	}
	p, err := probeStreamsUncached(ctx, runtime.config, scope.path, false)
	if err != nil {
		return mediaexport.Input{}, nil, err
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
		return mediaexport.Input{}, nil, &mediaerror.Problem{Code: "audio_track_unavailable", Message: "replacement recording has no selected audio stream; inspect its audio tracks"}
	}
	if selected.Channels < 1 || selected.Channels > 8 {
		return mediaexport.Input{}, nil, errors.New("replacement audio requires between 1 and 8 channels")
	}
	clock := int64(0)
	if p.Format.StartTime != "" && p.Format.StartTime != "N/A" {
		value, err := strconv.ParseFloat(p.Format.StartTime, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 30*24*3600 {
			return mediaexport.Input{}, nil, mediainfo.ErrUnsupported
		}
		clock = int64(math.Round(value * 1000))
	}
	if err := CheckAudioReplacement(ctx); err != nil {
		return mediaexport.Input{}, nil, err
	}
	return mediaexport.Input{Path: scope.path, OffsetMS: options.OffsetMS - clock}, selected, nil
}
