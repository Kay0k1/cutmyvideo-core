// Package mediaexport coordinates local or already-guarded media inputs.
// It performs no I/O itself; callers retain source authorization, sessions,
// tool execution, storage admission and durable publication.
package mediaexport

import (
	"context"
	"errors"
	"fmt"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type Config struct {
	FFmpegThreads  int
	FFmpegProfile  string
	MaxOutputBytes int64
}

type Input struct {
	Path     string
	Remote   bool
	OffsetMS int64
}

type Keyframe struct {
	PTS, DTS int64
	HasDTS   bool
}

type Selection struct {
	Video, Audio           *mediainfo.Stream
	VideoInput, AudioInput int
}

func (s Selection) MapArgs(format string) []string {
	var args []string
	if format != "mp3" && s.Video != nil {
		args = append(args, "-map", fmt.Sprintf("%d:%d", s.VideoInput, s.Video.Index))
	}
	if s.Audio != nil {
		args = append(args, "-map", fmt.Sprintf("%d:%d", s.AudioInput, s.Audio.Index))
	}
	return args
}

// Runtime is the effect boundary for one export. Implementations preserve
// caller-specific process, cache, filesystem and diagnostic policies.
type Runtime interface {
	SelectInputs(context.Context, []Input, edit.ExportRequest) (Selection, error)
	NearestKeyframe(context.Context, string, int64, bool, int) (Keyframe, error)
	Run(context.Context, []string, int64, func(int64)) error
	StatSize(string) (int64, error)
	Probe(context.Context, string, bool) (mediainfo.Info, int64, error)
	CopyDiagnostic(error, string) error
	RenderedFailure(error, bool) error
}

var ErrNoAudio = errors.New("source has no audio stream")
var ErrOutputLimit = errors.New("output exceeds size limit")
