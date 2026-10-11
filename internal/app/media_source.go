package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Refresh operation metadata before reserving decoder capacity. Historical
// local rows can describe a cover image instead of the selected playable video.
// Ordinary source reads remain metadata-only, and duration/retention are kept.
func (s *Store) refreshLocalMediaDimensions(ctx context.Context, c Config, source Source) (Source, error) {
	if err := ctx.Err(); err != nil {
		return source, err
	}
	if source.Path == "" {
		return source, nil
	}
	file, err := os.Stat(source.Path)
	if err != nil {
		return source, err
	}
	if !file.Mode().IsRegular() {
		return source, errUnsupportedStream
	}
	timeout := c.SourceTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	inspectionCtx, cancel := context.WithTimeout(ctx, timeout)
	inspected, err := InspectLocal(inspectionCtx, c, source.Path)
	inspectionErr := inspectionCtx.Err()
	cancel()
	if inspectionErr != nil {
		return source, errors.Join(inspectionErr, err)
	}
	if err != nil {
		if !unknownLocalMediaDimensions(err) {
			return source, err
		}
		// An unreadable media shape must not inherit a low weight from old
		// metadata. The actual rendering probe will still reject invalid input.
		inspected.Width, inspected.Height = 0, 0
	}
	if err := ctx.Err(); err != nil {
		return source, err
	}
	source.Tracks = inspected.Tracks
	if source.Width == inspected.Width && source.Height == inspected.Height {
		return source, nil
	}
	// Begin the short SQL budget after probing. This identity guard prevents a
	// stale caller from modifying a source deleted or replaced during inspection.
	err = apiDatabaseExec(ctx, func(dbCtx context.Context) error {
		changed, err := s.DB.Exec(dbCtx, "UPDATE sources SET width=$1,height=$2 WHERE id=$3 AND owner=$4 AND path=$5", inspected.Width, inspected.Height, source.ID, source.Owner, source.Path)
		if err != nil {
			return err
		}
		if changed.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return source, err
	}
	source.Width, source.Height = inspected.Width, inspected.Height
	return source, nil
}

// A malformed finite-media snapshot can be scheduled conservatively without
// concealing cancellation, unavailable tools, storage or operating-system errors.
func unknownLocalMediaDimensions(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) ||
		errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) ||
		errors.Is(err, errProcessResponseTooLarge) {
		return false
	}
	var tool *exec.Error
	if errors.As(err, &tool) {
		return false
	}
	var process *mediaProcessFailure
	if errors.As(err, &process) && process.category != "unsupported_media" && process.category != "unknown" {
		return false
	}
	var inspection *localInspectionFailure
	return errors.As(err, &inspection)
}
