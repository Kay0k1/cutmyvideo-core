package app

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type joinedMediaRuntime struct{ mediaExportRuntime }

func newJoinedMediaRuntime(c Config) joinedMediaRuntime {
	return joinedMediaRuntime{mediaExportRuntime: mediaExportRuntime{config: c}}
}

var _ mediaexport.JoinedRuntime = joinedMediaRuntime{}

func (joinedMediaRuntime) JoinPath(parts ...string) string { return filepath.Join(parts...) }
func (joinedMediaRuntime) BaseDir(path string) string      { return filepath.Dir(path) }
func (joinedMediaRuntime) ResolveLocalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(absolute), nil
}
func (joinedMediaRuntime) NewWorkspace(parent, prefix string) (string, error) {
	return os.MkdirTemp(parent, prefix)
}
func (joinedMediaRuntime) RemoveAll(path string) error          { return os.RemoveAll(path) }
func (joinedMediaRuntime) Remove(path string) error             { return os.Remove(path) }
func (joinedMediaRuntime) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
func (joinedMediaRuntime) WritePlan(path string, data []byte) error {
	return os.WriteFile(path, data, 0600)
}

type joinedMediaFile struct{ file os.FileInfo }

func (file *joinedMediaFile) Size() int64 { return file.file.Size() }
func (file *joinedMediaFile) Same(other mediaexport.JoinedFile) bool {
	value, ok := other.(*joinedMediaFile)
	return ok && os.SameFile(file.file, value.file) && file.file.Size() == value.file.Size() && file.file.ModTime().Equal(value.file.ModTime())
}
func (joinedMediaRuntime) StatFile(path string) (mediaexport.JoinedFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &joinedMediaFile{file: info}, nil
}

type joinedMediaSource struct{ session *LocalExportSession }

func (source *joinedMediaSource) Check() error {
	source.session.Mu.Lock()
	_, err := source.session.unchanged()
	source.session.Mu.Unlock()
	return err
}
func (joinedMediaRuntime) PinnedSource(ctx context.Context, input mediaexport.Input) mediaexport.SourceFence {
	session := localExportSession(ctx, input.Path, input.Remote)
	if session == nil {
		return nil
	}
	return &joinedMediaSource{session: session}
}
func (runtime joinedMediaRuntime) ProbeStreams(ctx context.Context, path string, remote bool) (mediainfo.Info, error) {
	info, err := probeStreams(ctx, runtime.config, path, remote)
	return mediainfo.Info(info), err
}
func (runtime joinedMediaRuntime) MuxSubtitles(ctx context.Context, source string, info mediainfo.Info, media, out string, segments []edit.Segment, request edit.ExportRequest, limit int64) error {
	return muxTextSubtitles(ctx, runtime.config, source, probeInfo(info), media, out, segments, ExportRequest(request), limit)
}
func (runtime joinedMediaRuntime) VerifyAccurate(ctx context.Context, out string, request edit.ExportRequest, expected, limit int64, selection mediaexport.Selection) (mediainfo.Info, error) {
	original := mediaSelection{Video: selection.Video, Audio: selection.Audio, VideoInput: selection.VideoInput, AudioInput: selection.AudioInput, mp3DurationMode: selection.MP3DurationMode}
	var info probeInfo
	var err error
	if request.Format == "mp4" && runtime.config.FFmpegProfile != "compact" {
		info, err = verifyJoinedOwnedMP4(ctx, runtime.config, out, ExportRequest(request), expected, limit, original)
	} else {
		info, err = verifyJoinedOwnedMP3(ctx, runtime.config, out, ExportRequest(request), expected, limit, original.mp3DurationMode)
	}
	return mediainfo.Info(info), err
}
func (runtime joinedMediaRuntime) SupportsAudioPacketDrop(ctx context.Context) (bool, error) {
	return supportsAudioPacketDrop(ctx, runtime.config)
}
