package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

// Inspection may discover that persisted dimensions describe a small cover
// instead of the playable stream. Persist its conservative weight in the
// existing lease token so admission across API processes keeps the full pool.
const previewWeightSQL = `CASE WHEN r.token LIKE 'inspection-full:%' THEN 2 WHEN s.width>0 AND s.height>0 AND s.width<=8388608/GREATEST(1,s.height) THEN 1 ELSE 2 END`

var errPreviewPending = errors.New("this preview is being prepared")

var errPreviewNeedsInspection = errors.New("preview admission needs selected local dimensions")

var errPreviewAdmissionUncertain = errors.New("preview admission commit outcome unknown")

var ErrPreviewCommitUncertain = errors.New("preview publication commit outcome unknown")

func (s *Store) reservePreview(ctx context.Context, c Config, source Source, id string) error {
	file, err := s.acquirePreview(ctx, c, source, id, id, false)
	if file != nil {
		file.Close()
	}
	return err
}

// Admission and cache lookup share the storage lock, so two API processes can
// never render the same interval concurrently. Cached descriptors remain valid
// after eviction; no source is pinned by a completed cache entry.
func (s *Store) acquirePreview(ctx context.Context, c Config, source Source, id, key string, background bool) (*os.File, error) {
	return s.acquirePreviewAfterCacheLookup(ctx, c, source, id, key, background, false)
}

func (s *Store) acquirePreviewAfterCacheLookup(ctx context.Context, c Config, source Source, id, key string, background, needsLocalInspection bool) (*os.File, error) {
	if err := s.bootstrapStorage(ctx, c); err != nil {
		return nil, err
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackStorage(tx)
	if err = checkSourceMedia(ctx, tx, source.ID, source.Owner); err != nil {
		return nil, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sources WHERE id=$1 AND owner=$2)", source.ID, source.Owner).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	var cachedID string
	err = tx.QueryRow(ctx, "SELECT id FROM storage_reservations WHERE kind='preview_cache' AND owner=$1 AND job_id=$2 AND token=$3 AND expires_at>clock_timestamp() LIMIT 1", source.Owner, source.ID, key).Scan(&cachedID)
	if err == nil {
		path, pathErr := previewDirectory(c, cachedID)
		if pathErr != nil {
			return nil, pathErr
		}
		filename := "preview.mp4"
		limit := previewOutputBytes
		if strings.HasPrefix(key, "storyboard:") {
			filename, limit = "storyboard.jpg", storyboardOutputBytes
		} else if strings.HasPrefix(key, "waveform:") {
			filename, limit = "waveform.json", waveformOutputBytes
		}
		file, openErr := os.Open(filepath.Join(path, filename))
		if openErr == nil {
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() >= limit {
				file.Close()
				openErr = errors.New("invalid preview cache file")
			} else {
				_, err = tx.Exec(ctx, "UPDATE storage_reservations SET expires_at=clock_timestamp()+($2*interval '1 second') WHERE id=$1", cachedID, previewCacheTTL.Seconds())
				if err == nil {
					err = tx.Commit(ctx)
				}
				if err != nil {
					file.Close()
					return nil, err
				}
				return file, nil
			}
		}
		if openErr != nil && !os.IsNotExist(openErr) {
			return nil, openErr
		}
		if err = removePreviewEntry(ctx, tx, c, cachedID); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var pending bool
	var active, weight int
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(`+previewWeightSQL+`),0),COALESCE(bool_or(r.job_id=$1 AND r.token=$2),false) FROM storage_reservations r LEFT JOIN sources s ON s.id=r.job_id WHERE r.kind IN ('preview','poster')`, source.ID, key).Scan(&active, &weight, &pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, errPreviewPending
	}
	// Background images/prefetch must yield before even opening a decoder
	// for inspection, rather than consuming work before a later rejection.
	if background && active >= 1 {
		return nil, errSourceServerBusy
	}
	if needsLocalInspection && source.Path != "" {
		return nil, errPreviewNeedsInspection
	}
	// Large/unknown inputs can use much more decoder RAM than the 480p
	// result. Platforms can also fall back to a larger rendition. Give those
	// sources the whole pool, as workers do.
	needed := 1
	if source.Width <= 0 || source.Height <= 0 || source.Width > 8_388_608/source.Height {
		needed = 2
	}
	if weight+needed > 2 || background && active >= 1 {
		return nil, errSourceServerBusy
	}
	if err = prunePreviewCache(ctx, tx, c, source.Owner, 0, false); err != nil {
		return nil, err
	}
	if c.MaxStorageBytes > 0 {
		fits, err := storageFits(ctx, tx, c, previewReservationBytes, "")
		if err != nil {
			return nil, err
		}
		if !fits {
			// Preview cache is disposable: release it before rejecting useful
			// work for lack of disk space.
			if err = prunePreviewCache(ctx, tx, c, "", 0, true); err != nil {
				return nil, err
			}
			fits, err = storageFits(ctx, tx, c, previewReservationBytes, "")
			if err != nil {
				return nil, err
			}
			if !fits {
				return nil, errSourceStorage
			}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id,token) VALUES($1,$2,'preview',$3,clock_timestamp()+($4*interval '1 second'),$5,$6)`, id, source.Owner, previewReservationBytes, previewReservationTTL.Seconds(), source.ID, key)
	if err != nil {
		return nil, err
	}
	return nil, commitPreviewAdmission(ctx, tx)
}

func commitPreviewAdmission(ctx context.Context, tx pgx.Tx) error {
	err := tx.Commit(ctx)
	if err != nil && !publicationCommitRejected(err) {
		return errors.Join(errPreviewAdmissionUncertain, err)
	}
	return err
}

func (s *Server) releasePreviewAdmission(source Source, id string, workspace bool) {
	ctx, cancel := context.WithTimeout(context.Background(), workerDatabaseTimeout)
	defer cancel()
	_ = s.Store.releasePreviewAdmission(ctx, s.Config, source, id, workspace)
}

// The admission transaction may still be finishing after losing its COMMIT
// acknowledgement. Take its storage fence before reading/deleting the own
// lease. Completed cache entries and other owners are never removed here.
func (s *Store) releasePreviewAdmission(ctx context.Context, c Config, source Source, id string, workspace bool) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE id=$1 AND owner=$2 AND job_id=$3 AND kind='preview')", id, source.Owner, source.ID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return tx.Commit(ctx)
	}
	if workspace {
		dir, err := previewDirectory(c, id)
		if err != nil {
			return err
		}
		if err = removeStorageTree(ctx, dir); err != nil {
			return err
		}
		if err = fsdurable.SyncDirectories(filepath.Dir(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND owner=$2 AND job_id=$3 AND kind='preview'", id, source.Owner, source.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) publishPreview(ctx context.Context, c Config, source Source, id string, file *os.File) error {
	return s.publishPreviewWithSync(ctx, c, source, id, file, fsdurable.Sync)
}

func (s *Store) publishPreviewWithSync(ctx context.Context, c Config, source Source, id string, file *os.File, synchronize func(string, ...string) (fs.FileInfo, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := previewDirectory(c, id)
	if err != nil {
		return err
	}
	filename := filepath.Base(file.Name())
	if filename != "preview.mp4" && filename != "storyboard.jpg" && filename != "waveform.json" {
		return errors.New("preview descriptor does not name a supported window asset")
	}
	path := filepath.Join(dir, filename)
	if filepath.Clean(file.Name()) != filepath.Clean(path) {
		return errors.New("preview descriptor does not name its reserved output")
	}
	info, err := synchronize(path, filepath.Dir(dir), c.DataDir, filepath.Dir(c.DataDir))
	if err != nil {
		return err
	}
	descriptor, err := file.Stat()
	if err != nil {
		return err
	}
	limit := previewOutputBytes
	if filename == "storyboard.jpg" {
		limit = storyboardOutputBytes
	} else if filename == "waveform.json" {
		limit = waveformOutputBytes
	}
	if !os.SameFile(info, descriptor) || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() >= limit {
		return errors.New("invalid completed preview file")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return apiDatabaseExec(ctx, func(databaseCtx context.Context) error {
		tx, err := s.storageTx(databaseCtx)
		if err != nil {
			return err
		}
		return publishPreviewTransaction(databaseCtx, tx, c, source, id, info.Size())
	})
}

func publishPreviewTransaction(ctx context.Context, tx pgx.Tx, c Config, source Source, id string, size int64) error {
	defer rollbackStorage(tx)
	if err := prunePreviewCache(ctx, tx, c, source.Owner, size, false); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE storage_reservations SET kind='preview_cache',size_bytes=$2,expires_at=clock_timestamp()+($3*interval '1 second') WHERE id=$1 AND kind='preview' AND owner=$4 AND job_id=$5 AND expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM sources WHERE id=$5 AND owner=$4)`, id, size, previewCacheTTL.Seconds(), source.Owner, source.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("preview lease expired")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil && !publicationCommitRejected(err) {
		return errors.Join(ErrPreviewCommitUncertain, err)
	}
	return err
}
