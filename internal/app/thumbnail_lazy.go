package app

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

type thumbnailFetcher func(context.Context, Config, string, string, int64) (string, error)
type thumbnailResolver func(context.Context, Source) (platformInfo, error)

var errThumbnailReady = errors.New("thumbnail was already prepared")

func (s *Server) resolveThumbnailMetadata(ctx context.Context, source Source) (platformInfo, error) {
	g, err := newNetworkGuard(32 << 20)
	if err != nil {
		return platformInfo{}, err
	}
	defer g.Close()
	info, _, err := s.Store.ResolvePlatformMetadata(ctx, s.Config, source, g)
	return info, err
}

// The caller has already authorized and pinned the source. Only this bounded
// GET prepares an image; import creates no image task or detached goroutine.
func (s *Server) prepareLazyThumbnail(parent context.Context, source Source, fetch thumbnailFetcher, resolve thumbnailResolver) (Source, error) {
	if (source.Kind != "platform" && source.Kind != "youtube") || source.ThumbnailURL == nil {
		return source, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	var allowance int64
	for {
		err := s.beginSourceReservation(ctx, source.Owner, func(dbCtx context.Context) error {
			var err error
			allowance, err = s.Store.reserveLazyThumbnail(dbCtx, s.Config, source, s.sourceToken(source.Owner))
			return err
		})
		if err == nil {
			break
		}
		if errors.Is(err, errThumbnailReady) {
			return s.databaseSource(ctx, source.ID, source.Owner)
		}
		if !errors.Is(err, errSourceBusy) {
			return source, err
		}
		// Import may have returned just before its deferred reservation release.
		// A second image GET may also be completing the same source. Wait within
		// the single preparation deadline and reuse an already attached file.
		select {
		case <-ctx.Done():
			return source, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
		fresh, err := s.databaseSource(ctx, source.ID, source.Owner)
		if err != nil || fresh.MediaRemoved {
			if err == nil {
				err = ErrSourceMediaRemoved
			}
			return source, err
		}
		if fresh.ThumbnailPath != "" {
			return fresh, nil
		}
	}
	s.sourceIdentity(source.Owner, source.ID)
	defer s.endSource(source.Owner)
	info, err := resolve(ctx, source)
	if err != nil {
		return source, err
	}
	info, err = freshPlatformMetadata(source, func() (platformInfo, error) { return info, nil })
	if err != nil {
		return source, err
	}
	path, err := fetch(ctx, s.Config, source.ID, info.Thumbnail, allowance)
	if err != nil {
		return source, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = removeSourcePreparation(path)
		}
	}()
	err = s.Store.attachLazyThumbnail(ctx, s.Config, source, s.sourceToken(source.Owner), path, allowance)
	if err != nil {
		if errors.Is(err, ErrSourceCommitUncertain) {
			keep = true
			s.holdSourceReservation(source.Owner)
		}
		return source, err
	}
	keep = true
	source.ThumbnailPath = path
	completeSourcePresentation(&source)
	return source, nil
}

func (s *Store) reserveLazyThumbnail(ctx context.Context, c Config, source Source, token string) (int64, error) {
	if err := s.bootstrapStorage(ctx, c); err != nil {
		return 0, err
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackStorage(tx)
	if err := checkSourceMedia(ctx, tx, source.ID, source.Owner); err != nil {
		return 0, err
	}
	var path string
	err = tx.QueryRow(ctx, `SELECT thumbnail_path FROM sources WHERE id=$1 AND owner=$2 AND url=$3 AND provider_id=$4 AND duration_ms=$5 AND kind IN ('platform','youtube') FOR UPDATE`, source.ID, source.Owner, source.URL, source.ProviderID, source.DurationMS).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if path != "" {
		return 0, errThumbnailReady
	}
	var busy bool
	var active int
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE id=$1),(SELECT count(*) FROM storage_reservations WHERE kind='source')`, "source:"+source.Owner).Scan(&busy, &active); err != nil {
		return 0, err
	}
	if busy {
		return 0, errSourceBusy
	}
	if active >= 4 {
		return 0, errSourceServerBusy
	}
	var allowance int64
	err = tx.QueryRow(ctx, `SELECT LEAST($1::numeric,CASE WHEN $2::numeric>0 THEN GREATEST(0,$2::numeric-(SELECT COALESCE(sum(size_bytes),0) FROM storage_files WHERE owner=$4 AND kind='source')) ELSE $1::numeric END,CASE WHEN $3::numeric>0 THEN GREATEST(0,$3::numeric-stored_bytes-reserved_bytes) ELSE $1::numeric END)::bigint FROM storage_counters WHERE id=1`, maxThumbnailBytes, c.MaxOwnerBytes, c.MaxStorageBytes, source.Owner).Scan(&allowance)
	if err != nil {
		return 0, err
	}
	if allowance <= 0 {
		return 0, errSourceStorage
	}
	if c.MaxStorageBytes > 0 {
		fits, err := storageFits(ctx, tx, c, allowance, "")
		if err != nil {
			return 0, err
		}
		if !fits {
			return 0, errSourceStorage
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,token) VALUES($1,$2,'source',$3,clock_timestamp()+interval '1 minute',$4)`, "source:"+source.Owner, source.Owner, allowance, token)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		// beginSourceReservation releases/adopts under its exact token even if
		// the reservation's COMMIT acknowledgement was lost.
		return 0, err
	}
	return allowance, nil
}

func (s *Store) attachLazyThumbnail(ctx context.Context, c Config, source Source, token, path string, allowance int64) error {
	return s.attachLazyThumbnailWithCommit(ctx, c, source, token, path, allowance, func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) })
}

func (s *Store) attachLazyThumbnailWithCommit(ctx context.Context, c Config, source Source, token, path string, allowance int64, commit func(context.Context, pgx.Tx) error) error {
	if path != filepath.Join(c.DataDir, "sources", source.ID+".thumbnail") {
		return errors.New("thumbnail path does not belong to this source")
	}
	info, err := fsdurable.Sync(path, publicationParents(c, path)...)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > allowance || allowance > maxThumbnailBytes {
		return errors.New("thumbnail exceeds its reservation")
	}
	return apiDatabaseExec(ctx, func(ctx context.Context) error {
		tx, err := s.storageTx(ctx)
		if err != nil {
			return err
		}
		defer rollbackStorage(tx)
		if err = checkSourceMedia(ctx, tx, source.ID, source.Owner); err != nil {
			return err
		}
		var current string
		err = tx.QueryRow(ctx, `SELECT thumbnail_path FROM sources WHERE id=$1 AND owner=$2 AND url=$3 AND provider_id=$4 AND duration_ms=$5 AND kind IN ('platform','youtube') AND path='' FOR UPDATE`, source.ID, source.Owner, source.URL, source.ProviderID, source.DurationMS).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current != "" {
			return errThumbnailReady
		}
		tag, err := tx.Exec(ctx, `DELETE FROM storage_reservations WHERE id=$1 AND token=$2 AND size_bytes=$3 AND expires_at>clock_timestamp()`, "source:"+source.Owner, token, allowance)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if err = registerStorageFile(ctx, tx, path, source.Owner, "source", source.ID, info.Size()); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE sources SET thumbnail_path=$1,thumbnail_url=$2 WHERE id=$3 AND owner=$4`, path, "/api/v1/sources/"+source.ID+"/thumbnail", source.ID, source.Owner); err != nil {
			return err
		}
		if err = commit(ctx, tx); err != nil && !publicationCommitRejected(err) {
			return errors.Join(ErrSourceCommitUncertain, err)
		}
		return err
	})
}
