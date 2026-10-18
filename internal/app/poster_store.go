package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/coremodel"
	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
	"github.com/jackc/pgx/v5"
)

type ImageArtifact = coremodel.ImageArtifact

var errPosterCommitUncertain = errors.New("poster publication commit outcome unknown")

type posterLease struct {
	id, owner, parent, path, format string
	expiry                          *time.Time
}

const imageArtifactSelect = `SELECT i.id,i.parent_artifact_id,i.filename,i.format,i.size_bytes,i.width,i.height,i.timestamp_ms,i.expires_at,i.path
 FROM image_artifacts i JOIN artifacts a ON a.id=i.parent_artifact_id AND a.owner=i.owner JOIN jobs j ON j.id=a.job_id AND j.owner=a.owner
 WHERE i.owner=$1 AND (i.expires_at IS NULL OR i.expires_at>clock_timestamp())
 AND (a.expires_at IS NULL OR a.expires_at>clock_timestamp()) AND j.status NOT IN ('queued','running','waiting_storage')`

func scanImage(row pgx.Row) (a ImageArtifact, path string, err error) {
	err = row.Scan(&a.ID, &a.ParentArtifactID, &a.Filename, &a.Format, &a.SizeBytes, &a.Width, &a.Height, &a.TimestampMS, &a.ExpiresAt, &path)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		a.DownloadURL = "/api/v1/image-artifacts/" + a.ID + "/download"
	}
	return
}

func (s *Store) imageArtifact(ctx context.Context, id, owner string) (ImageArtifact, string, error) {
	return scanImage(s.DB.QueryRow(ctx, imageArtifactSelect+" AND i.id=$2", owner, id))
}

func posterPath(c Config, id, format string) string {
	ext := ".jpg"
	if format == "png" {
		ext = ".png"
	}
	return filepath.Join(c.DataDir, "artifacts", id+ext)
}
func posterWorkspace(c Config, id string) string { return filepath.Join(c.DataDir, "work", id) }

func (s *Store) acquirePoster(ctx context.Context, c Config, owner, parent, id string, o localmedia.PosterOptions) (lease posterLease, existing *ImageArtifact, err error) {
	if err = s.bootstrapStorage(ctx, c); err != nil {
		return
	}
	tx, e := s.storageTx(ctx)
	if e != nil {
		err = e
		return
	}
	defer rollbackStorage(tx)
	if err = s.cleanupPosterState(ctx, tx, c); err != nil {
		return
	}
	a, _, e := scanImage(tx.QueryRow(ctx, imageArtifactSelect+" AND i.parent_artifact_id=$2 AND i.timestamp_ms=$3 AND i.format=$4", owner, parent, o.TimestampMS, o.Format))
	if e == nil {
		existing = &a
		err = tx.Commit(ctx)
		return
	}
	if !errors.Is(e, ErrNotFound) {
		err = e
		return
	}
	lease = posterLease{id: id, owner: owner, parent: parent, format: o.Format}
	err = tx.QueryRow(ctx, `SELECT a.path,CASE WHEN a.expires_at IS NOT NULL THEN a.expires_at WHEN $3::double precision>0 THEN a.created_at+($3*interval '1 second') ELSE NULL END
 FROM artifacts a JOIN jobs j ON j.id=a.job_id AND j.owner=a.owner WHERE a.id=$1 AND a.owner=$2
 AND j.status NOT IN ('queued','running','waiting_storage') AND (a.expires_at IS NULL OR a.expires_at>clock_timestamp())
 AND (a.expires_at IS NOT NULL OR $3::double precision<=0 OR a.created_at>clock_timestamp()-($3*interval '1 second')) FOR UPDATE OF a`, parent, owner, c.ArtifactTTL.Seconds()).Scan(&lease.path, &lease.expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return
	}
	var active int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM storage_reservations WHERE kind IN ('preview','poster')").Scan(&active); err != nil {
		return
	}
	if active > 0 {
		err = errSourceServerBusy
		return
	}
	reserve := min(localmedia.PosterMaxBytes, c.MaxOutputBytes)
	if reserve <= 0 {
		reserve = localmedia.PosterMaxBytes
	}
	if c.MaxStorageBytes > 0 {
		var fits bool
		fits, err = storageFits(ctx, tx, c, reserve, "")
		if err != nil {
			return
		}
		if !fits {
			err = errSourceStorage
			return
		}
	}
	if c.MaxOwnerBytes > 0 {
		var fits bool
		err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(size_bytes) FROM storage_files WHERE owner=$1),0)+COALESCE((SELECT sum(size_bytes) FROM storage_reservations WHERE owner=$1),0)+$2::numeric<=$3::numeric`, owner, reserve, c.MaxOwnerBytes).Scan(&fits)
		if err != nil {
			return
		}
		if !fits {
			err = errSourceStorage
			return
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO artifact_readers(id,artifact_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '60 seconds')`, id, parent)
	if err != nil {
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id,token) VALUES($1,$2,'poster',$3,clock_timestamp()+interval '60 seconds',$4,$5)`, "poster:"+id, owner, reserve, parent, o.Format)
	if err != nil {
		return
	}
	err = commitPreviewAdmission(ctx, tx)
	return
}

func (s *Store) publishPoster(ctx context.Context, c Config, lease posterLease, result localmedia.PosterResult) (ImageArtifact, error) {
	return s.publishPosterWithCommit(ctx, c, lease, result, func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) })
}

func (s *Store) publishPosterWithCommit(ctx context.Context, c Config, lease posterLease, result localmedia.PosterResult, commit func(context.Context, pgx.Tx) error) (ImageArtifact, error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return ImageArtifact{}, err
	}
	defer rollbackStorage(tx)
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_reservations r JOIN artifact_readers reader ON reader.id=$1 AND reader.artifact_id=$3 JOIN artifacts a ON a.id=$3 AND a.owner=$2 JOIN jobs j ON j.id=a.job_id AND j.owner=a.owner
 WHERE r.id=$4 AND r.owner=$2 AND r.kind='poster' AND r.job_id=$3 AND r.token=$5 AND r.expires_at>clock_timestamp() AND reader.expires_at>clock_timestamp()
 AND a.path=$6 AND (a.expires_at IS NULL OR a.expires_at>clock_timestamp()) AND j.status NOT IN ('queued','running','waiting_storage'))`, lease.id, lease.owner, lease.parent, "poster:"+lease.id, lease.format, lease.path).Scan(&valid)
	if err != nil {
		return ImageArtifact{}, err
	}
	if !valid {
		return ImageArtifact{}, ErrNotFound
	}
	path := posterPath(c, lease.id, lease.format)
	if result.Path != path || result.Format != lease.format || result.SizeBytes <= 0 || result.SizeBytes > localmedia.PosterMaxBytes || c.MaxOutputBytes > 0 && result.SizeBytes > c.MaxOutputBytes || result.Width <= 0 || result.Height <= 0 || result.Width > localmedia.PosterMaxPixels/result.Height {
		return ImageArtifact{}, localmedia.ErrInvalidPoster
	}
	_, err = tx.Exec(ctx, `INSERT INTO image_artifacts(id,owner,parent_artifact_id,path,filename,format,size_bytes,width,height,timestamp_ms,expires_at)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,LEAST($11::timestamptz,a.expires_at) FROM artifacts a WHERE a.id=$3 AND a.owner=$2`, lease.id, lease.owner, lease.parent, path, filepath.Base(path), lease.format, result.SizeBytes, result.Width, result.Height, result.TimestampMS, lease.expiry)
	if err != nil {
		return ImageArtifact{}, err
	}
	if err = registerStorageFile(ctx, tx, path, lease.owner, "image_artifact", lease.id, result.SizeBytes); err != nil {
		return ImageArtifact{}, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1", "poster:"+lease.id); err != nil {
		return ImageArtifact{}, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM artifact_readers WHERE id=$1", lease.id); err != nil {
		return ImageArtifact{}, err
	}
	if err = ctx.Err(); err != nil {
		return ImageArtifact{}, err
	}
	if err = commit(ctx, tx); err != nil {
		if !publicationCommitRejected(err) {
			err = errors.Join(errPosterCommitUncertain, err)
		}
		return ImageArtifact{}, err
	}
	a, _, err := s.imageArtifact(ctx, lease.id, lease.owner)
	return a, err
}

// A lost commit acknowledgement never authorizes removal of a published image.
// Recheck registration under the publication fence before retiring owned files.
func (s *Store) releasePoster(ctx context.Context, c Config, lease posterLease) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	var present bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM image_artifacts WHERE id=$1 AND owner=$2)", lease.id, lease.owner).Scan(&present); err != nil {
		return err
	}
	if !present {
		if err = tombstoneStorageFiles(ctx, tx, []storageDeletionFile{{path: posterPath(c, lease.id, lease.format), owner: lease.owner}}); err != nil {
			return err
		}
	}
	if err = removePosterWorkspace(ctx, c, lease.id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND owner=$2 AND kind='poster'", "poster:"+lease.id, lease.owner); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM artifact_readers WHERE id=$1", lease.id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) cleanupPosterState(ctx context.Context, tx pgx.Tx, c Config) error {
	rows, err := tx.Query(ctx, `SELECT id,owner,job_id,token FROM storage_reservations WHERE kind='poster' AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT $1`, storageBatch)
	if err != nil {
		return err
	}
	var expired []posterLease
	for rows.Next() {
		var raw string
		var l posterLease
		if err = rows.Scan(&raw, &l.owner, &l.parent, &l.format); err != nil {
			rows.Close()
			return err
		}
		l.id = strings.TrimPrefix(raw, "poster:")
		expired = append(expired, l)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, l := range expired {
		if !strings.HasPrefix(l.id, "img_") || strings.ContainsAny(l.id, "/\\") || (l.format != "jpeg" && l.format != "png") {
			return errors.New("invalid poster reservation")
		}
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM image_artifacts WHERE id=$1)", l.id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if err = tombstoneStorageFiles(ctx, tx, []storageDeletionFile{{path: posterPath(c, l.id, l.format), owner: l.owner}}); err != nil {
				return err
			}
		}
		if err = removePosterWorkspace(ctx, c, l.id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1", "poster:"+l.id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM artifact_readers WHERE id IN(SELECT id FROM artifact_readers WHERE expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT $1)`, storageBatch)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM image_artifacts WHERE id IN(SELECT id FROM image_artifacts WHERE expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT $1)`, storageBatch)
	return err
}

func (s *Store) deleteImageArtifact(ctx context.Context, id, owner string) ([]string, error) {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackStorage(tx)
	tag, err := tx.Exec(ctx, "DELETE FROM image_artifacts WHERE id=$1 AND owner=$2", id, owner)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrNotFound
	}
	paths, err := pendingStoragePaths(ctx, tx)
	if err != nil {
		return nil, err
	}
	return paths, tx.Commit(ctx)
}

func openPosterParent(c Config, path string) (os.FileInfo, error) {
	if !storagePathInside(c.DataDir, path) {
		return nil, ErrNotFound
	}
	if _, err := storageDeletionParent(c, path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	return info, nil
}

func removePosterWorkspace(ctx context.Context, c Config, id string) error {
	if err := removeStorageTree(ctx, posterWorkspace(c, id)); err != nil {
		return err
	}
	err := fsdurable.SyncDirectories(filepath.Join(c.DataDir, "work"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
