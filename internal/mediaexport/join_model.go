package mediaexport

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// JoinedFile retains an adapter-owned identity receipt. Equality includes the
// original file identity, size and modification time; the coordinator only
// accounts its size and compares receipts when reusing a copied interval.
type JoinedFile interface {
	Size() int64
	Same(JoinedFile) bool
}

// SourceFence checks the already selected pinned source. It adds no new
// cancellation policy and leaves synchronization and filesystem identity to
// the caller that owns the source session.
type SourceFence interface {
	Check() error
}

// JoinedRuntime retains effect and authorization boundaries while joined
// planning, timeline accounting and operation order remain in this package.
type JoinedRuntime interface {
	Runtime
	JoinPath(...string) string
	BaseDir(string) string
	ResolveLocalPath(string) (string, error)
	NewWorkspace(string, string) (string, error)
	RemoveAll(string) error
	Remove(string) error
	Rename(string, string) error
	WritePlan(string, []byte) error
	StatFile(string) (JoinedFile, error)
	PinnedSource(context.Context, Input) SourceFence
	ProbeStreams(context.Context, string, bool) (mediainfo.Info, error)
	MuxSubtitles(context.Context, string, mediainfo.Info, string, string, []edit.Segment, edit.ExportRequest, int64) error
	VerifyAccurate(context.Context, string, edit.ExportRequest, int64, int64, Selection) (mediainfo.Info, error)
	SupportsAudioPacketDrop(context.Context) (bool, error)
}
