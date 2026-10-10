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

func publicationParents(c Config, path string) []string {
	if c.DataDir != "" {
		if relative, err := filepath.Rel(c.DataDir, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return []string{c.DataDir, filepath.Dir(c.DataDir)}
		}
	}
	return nil
}

// AddSource persists completed source and thumbnail files before its bounded
// registration transaction. Metadata-only remote sources need no local file.
// A lost COMMIT acknowledgement keeps both files and their storage charge.
func (s *Store) AddSource(ctx context.Context, v Source) error {
	return s.addSourceWithSync(ctx, v, fsdurable.Sync)
}

func (s *Store) addSourceWithSync(ctx context.Context, v Source, synchronize func(string, ...string) (fs.FileInfo, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if (v.Kind == "upload" || v.Kind == "direct") && v.Path == "" {
		return errors.New("local source requires a completed file")
	}
	c := s.storageConfig()
	sizes := make(map[string]int64)
	for _, path := range []string{v.Path, v.ThumbnailPath} {
		if path == "" {
			continue
		}
		if _, exists := sizes[path]; exists {
			continue
		}
		info, err := synchronize(path, publicationParents(c, path)...)
		if err != nil {
			return err
		}
		if info.Size() <= 0 {
			return errors.New("completed source file is empty")
		}
		sizes[path] = info.Size()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// File synchronization consumes the source preparation budget, not SQL's.
	return apiDatabaseExec(ctx, func(databaseCtx context.Context) error {
		tx, err := s.storageTx(databaseCtx)
		if err != nil {
			return err
		}
		return s.addSourceTransaction(databaseCtx, tx, v, sizes, c)
	})
}

func (s *Store) addSourceTransaction(ctx context.Context, tx pgx.Tx, v Source, sizes map[string]int64, c Config) error {
	defer rollbackStorage(tx)
	v.Title = normalizeSourceTitle(v.Title)
	_, err := tx.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,path,url,width,height,embed_url,thumbnail_url,provider_id,provider,thumbnail_path) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, v.ID, v.Owner, v.Title, v.DurationMS, v.Kind, v.Path, v.URL, v.Width, v.Height, v.EmbedURL, v.ThumbnailURL, v.ProviderID, v.Provider, v.ThumbnailPath)
	if err != nil {
		return err
	}
	for path, size := range sizes {
		if err = registerStorageFile(ctx, tx, path, v.Owner, "source", v.ID, size); err != nil {
			return err
		}
	}
	if v.StorageToken != "" {
		if err = adoptPreparationExtras(ctx, tx, c, v.ID); err != nil {
			return err
		}
		tag, e := tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND token=$2 AND expires_at>clock_timestamp()", "source:"+v.Owner, v.StorageToken)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil && !publicationCommitRejected(err) {
		return errors.Join(ErrSourceCommitUncertain, err)
	}
	return err
}

// The server owns these random source names. Generic fsdurable.Publish never
// removes a final name; rejected private preparations may remove their own files.
func publishSourceMedia(ctx context.Context, c Config, source *Source) error {
	stage := source.Path
	final := filepath.Join(c.DataDir, "sources", source.ID+".media")
	_, err := fsdurable.Publish(ctx, stage, final)
	if err == nil || errors.Is(err, fsdurable.ErrPublicationUncertain) {
		source.Path = final
	}
	if err != nil {
		return err
	}
	if err = os.Remove(stage); err != nil {
		return err
	}
	return fsdurable.SyncDirectories(filepath.Dir(stage))
}

func removeSourcePreparation(paths ...string) error {
	var result error
	seen := make(map[string]bool)
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
		result = errors.Join(result, fsdurable.SyncDirectories(filepath.Dir(path)))
	}
	return result
}
