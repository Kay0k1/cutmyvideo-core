package localmedia

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

func analysisSource(ctx context.Context, c Config, path string) (context.Context, *LocalExportSession, probeInfo, int64, error) {
	var info probeInfo
	if err := ctx.Err(); err != nil {
		return ctx, nil, info, 0, err
	}
	if path == "" {
		return ctx, nil, info, 0, analysisArgument("a local input file is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return ctx, nil, info, 0, err
	}
	file, err := os.Stat(path)
	if err != nil {
		return ctx, nil, info, 0, err
	}
	if !file.Mode().IsRegular() {
		return ctx, nil, info, 0, analysisArgument("analysis requires a regular local input file")
	}
	ctx, session := BeginLocalExport(ctx, path)
	timeout := c.SourceTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	inspectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	info, duration, err := probe(inspectCtx, c, path, false)
	if err != nil {
		_ = session.Close()
		return ctx, nil, info, 0, &mediaerror.InspectionFailure{Cause: err}
	}
	return ctx, session, info, duration, nil
}

type analysisOutput struct {
	writer io.Writer
	cancel context.CancelFunc
	err    error
}

func (w *analysisOutput) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
		// os/exec does not kill a child when a streaming writer refuses bytes.
		// Cancel its owned process group immediately, retaining the real cause.
		w.cancel()
	}
	return n, err
}

func runAnalysis(ctx context.Context, c Config, args []string, writer io.Writer) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &analysisOutput{writer: writer, cancel: cancel}
	err := runCommandOutput(runCtx, c.FFmpeg, args, output)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if output.err != nil {
		return output.err
	}
	return err
}

func analysisInputArgs(c Config, path string, start, end int64) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-xerror", "-max_alloc", "268435456", "-filter_threads", "1", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	return append(args, "-ss", seconds(start), "-i", path, "-t", seconds(end-start))
}

func analysisUnchanged(session *LocalExportSession) error {
	_, err := session.unchanged()
	return err
}
