package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const sourceSelect = `SELECT id,owner,title,duration_ms,kind,path,url,width,height,embed_url,thumbnail_url,provider_id,provider,thumbnail_path,created_at,EXISTS(SELECT 1 FROM jobs WHERE source_id=sources.id),media_removed,(SELECT fingerprint FROM source_fingerprints WHERE source_id=sources.id AND owner=sources.owner) FROM sources`

func scanSource(row pgx.Row, c Config) (Source, error) {
	var v Source
	var created time.Time
	var fingerprint []byte
	err := row.Scan(&v.ID, &v.Owner, &v.Title, &v.DurationMS, &v.Kind, &v.Path, &v.URL, &v.Width, &v.Height, &v.EmbedURL, &v.ThumbnailURL, &v.ProviderID, &v.Provider, &v.ThumbnailPath, &created, &v.RetentionBlocked, &v.MediaRemoved, &fingerprint)
	if err == nil && len(fingerprint) > 0 {
		err = json.Unmarshal(fingerprint, &v.Fingerprint)
	}
	if err == nil && c.SourceTTL > 0 && !v.RetentionBlocked {
		expires := created.Add(c.SourceTTL)
		v.ExpiresAt = &expires
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	completeSourcePresentation(&v)
	return v, err
}

func (s *Store) Source(ctx context.Context, id, owner string) (Source, error) {
	return scanSource(s.DB.QueryRow(ctx, sourceSelect+" WHERE id=$1 AND owner=$2", id, owner), s.storageConfig())
}

func (s *Store) Sources(ctx context.Context, owner string) ([]Source, error) {
	// One bounded snapshot supplies presentation and retention state for all
	// sources, without an extra database round trip for every listed source.
	rows, err := s.DB.Query(ctx, sourceSelect+" WHERE owner=$1 ORDER BY created_at DESC,id DESC LIMIT 20", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c := s.storageConfig()
	sources := make([]Source, 0, 20)
	for rows.Next() {
		v, e := scanSource(rows, c)
		if e != nil {
			return nil, e
		}
		sources = append(sources, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return sources, nil
}
