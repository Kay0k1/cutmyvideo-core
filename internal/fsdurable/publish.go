// Package fsdurable publishes completed local files without replacing an
// existing name. Success requires the filesystem to honor file and directory
// synchronization; it does not make temporary or remote storage persistent.
package fsdurable

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	ErrUnsupportedFilesystem = errors.New("filesystem does not support durable exclusive publication")
	ErrSyncFailed            = errors.New("output synchronization failed")
	ErrPublicationFailed     = errors.New("file publication failed")
	ErrPublicationUncertain  = errors.New("file publication outcome is uncertain")
)

type operations struct {
	syncFile func(*os.File) error
	syncDir  func(string) error
	link     func(string, string) error
	remove   func(string) error
}

func systemOperations() operations {
	return operations{
		syncFile: (*os.File).Sync,
		syncDir:  syncDirectory,
		link:     os.Link,
		remove:   os.Remove,
	}
}

// Preflight tests the capabilities used by Publish before an expensive encode.
// Both directories must exist. Probe names are private and never use the final
// output name. This also detects separate mounted filesystems for staging and
// publication; a successful probe is not a promise against later I/O failures.
func Preflight(stagingDir, destinationDir string) error {
	return preflight(stagingDir, destinationDir, systemOperations())
}

func preflight(stagingDir, destinationDir string, ops operations) (result error) {
	probe, err := os.CreateTemp(stagingDir, ".cutmy-sync-probe-")
	if err != nil {
		return fmt.Errorf("%w: create publication probe: %w", ErrSyncFailed, err)
	}
	source := probe.Name()
	private := ""
	defer func() {
		var cleanup error
		for _, path := range []string{source, filepath.Join(private, "file"), private} {
			if private == "" && path != source {
				continue
			}
			if err := ops.remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanup = errors.Join(cleanup, err)
			}
		}
		// Persist probe removal too: otherwise a crash can resurrect private
		// probe subdirectories that ordinary artifact reconciliation skips.
		for _, dir := range []string{stagingDir, destinationDir} {
			cleanup = errors.Join(cleanup, ops.syncDir(dir))
		}
		if cleanup != nil {
			result = errors.Join(result, fmt.Errorf("%w: clean publication probes: %w", ErrSyncFailed, cleanup))
		}
	}()
	if _, err = probe.Write([]byte{0}); err == nil {
		err = ops.syncFile(probe)
	}
	err = errors.Join(err, probe.Close())
	if err != nil {
		return capabilityError("synchronize publication probe", err)
	}
	private, err = os.MkdirTemp(destinationDir, ".cutmy-link-probe-")
	if err != nil {
		return fmt.Errorf("%w: create publication probe directory: %w", ErrSyncFailed, err)
	}
	if err = ops.link(source, filepath.Join(private, "file")); err != nil {
		return capabilityError("link publication probe", err)
	}
	if err = ops.syncDir(private); err != nil {
		return capabilityError("synchronize publication probe directory", err)
	}
	if err = ops.syncDir(destinationDir); err != nil {
		return capabilityError("synchronize output directory", err)
	}
	return nil
}

func capabilityError(operation string, err error) error {
	classification := ErrSyncFailed
	if unsupportedOperation(err) {
		classification = ErrUnsupportedFilesystem
	}
	return fmt.Errorf("%w: %s: %w", classification, operation, err)
}

// Publish synchronizes a completed regular staging file, links it exclusively
// at destination, and synchronizes the destination directory. Its returned
// metadata belongs to the staging file, rather than a later lookup of a name
// another process could replace. The caller still owns the staging name.
//
// A post-link synchronization failure retains the final name and returns
// ErrPublicationUncertain. No portable inode-conditional unlink exists: an
// identity check followed by removal could delete another caller's replacement.
// The caller must inspect the destination before deciding whether to retry.
func Publish(ctx context.Context, source, destination string) (fs.FileInfo, error) {
	return publish(ctx, source, destination, systemOperations())
}

func publish(ctx context.Context, source, destination string, ops operations) (fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(source, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open completed output: %w", ErrSyncFailed, err)
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("completed output is not a regular file")
	}
	if err == nil {
		err = ops.syncFile(file)
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, fmt.Errorf("%w: synchronize completed output: %w", ErrSyncFailed, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = ops.link(source, destination); err != nil {
		return nil, fmt.Errorf("%w: link completed output: %w", ErrPublicationFailed, err)
	}
	if err = ops.syncDir(filepath.Dir(destination)); err != nil {
		return nil, fmt.Errorf("%w: synchronize output directory: %w", ErrPublicationUncertain, err)
	}
	return info, nil
}

// Sync synchronizes file data before the containing directory and the supplied
// known parent directories. It never sweeps unrelated ancestors. This is the
// final filesystem barrier before a database may register an artifact.
func Sync(path string, directories ...string) (fs.FileInfo, error) {
	return syncPath(path, directories, systemOperations())
}

func syncPath(path string, directories []string, ops operations) (fs.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open published output: %w", ErrSyncFailed, err)
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("published output is not a regular file")
	}
	if err == nil {
		err = ops.syncFile(file)
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, fmt.Errorf("%w: synchronize published output: %w", ErrSyncFailed, err)
	}
	seen := make(map[string]bool)
	for _, dir := range append([]string{filepath.Dir(path)}, directories...) {
		dir = filepath.Clean(dir)
		if !seen[dir] {
			if err = ops.syncDir(dir); err != nil {
				return nil, fmt.Errorf("%w: synchronize artifact directory: %w", ErrSyncFailed, err)
			}
			seen[dir] = true
		}
	}
	return info, nil
}

// SyncDirectories persists a bounded set of known directory entries. Callers
// include the parent of a directory they have just created.
func SyncDirectories(directories ...string) error {
	for _, dir := range directories {
		if err := syncDirectory(dir); err != nil {
			return capabilityError("synchronize publication parent", err)
		}
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	info, err := dir.Stat()
	if err == nil && !info.IsDir() {
		err = errors.New("publication parent is not a directory")
	}
	if err == nil {
		err = dir.Sync()
	}
	return errors.Join(err, dir.Close())
}
