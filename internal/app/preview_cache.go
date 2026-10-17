package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
	"github.com/jackc/pgx/v5"
)

func previewCacheKey(source Source, start int64) string {
	return "display-v1:" + previewCacheKeyOptions(source, start, ExportRequest{})
}

func previewCacheKeyOptions(source Source, start int64, request ExportRequest) string {
	audio := "default"
	if request.AudioTrack != nil {
		audio = strconv.Itoa(*request.AudioTrack)
	}
	if request.NoAudio {
		audio = "none"
	}
	// A prior rendition may have selected different tracks or display/color
	// handling. Leave its charged cache entry to expire, without reusing it.
	contract := "media-contract-v3:"
	if source.Path == "" {
		if page, err := normalizeSourceURL(source.URL); err == nil && isPlatformHost(page.Hostname()) {
			contract += "original-audio-v1:"
		}
		if page, err := normalizeSourceURL(source.URL); err == nil && page.Hostname() == "clips.twitch.tv" {
			// Both video windows and storyboards must stop reusing the portrait
			// adaptation after the original Twitch clip selection is repaired.
			contract += "twitch-original-v1:"
		}
	}
	sum := sha256.Sum256([]byte(contract + source.ID + ":" + strconv.FormatInt(start, 10) + ":" + audio))
	return hex.EncodeToString(sum[:])
}

type previewCacheEntry struct {
	id, owner string
	size      int64
	expired   bool
}

func prunePreviewCache(ctx context.Context, tx pgx.Tx, c Config, owner string, incoming int64, all bool) error {
	rows, err := tx.Query(ctx, "SELECT id,owner,size_bytes,expires_at<=clock_timestamp() OR NOT EXISTS(SELECT 1 FROM sources WHERE id=job_id) FROM storage_reservations WHERE kind='preview_cache' ORDER BY expires_at LIMIT $1", storageBatch)
	if err != nil {
		return err
	}
	var entries []previewCacheEntry
	var total, owned int64
	var ownerCount int
	for rows.Next() {
		var entry previewCacheEntry
		if err := rows.Scan(&entry.id, &entry.owner, &entry.size, &entry.expired); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, entry)
		total += entry.size
		if entry.owner == owner {
			owned += entry.size
			ownerCount++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	count := len(entries)
	addition := 0
	if incoming > 0 {
		addition = 1
	}
	for _, entry := range entries {
		exceeds := total+incoming > previewCacheBytes || count+addition > previewCacheEntries
		ownerExceeds := entry.owner == owner && (owned+incoming > previewOwnerCacheBytes || ownerCount+addition > previewOwnerCacheEntries)
		if all || entry.expired || exceeds || ownerExceeds {
			if err := removePreviewEntry(ctx, tx, c, entry.id); err != nil {
				return err
			}
			total -= entry.size
			count--
			if entry.owner == owner {
				owned -= entry.size
				ownerCount--
			}
		}
	}
	return nil
}

func previewDirectory(c Config, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return "", fmt.Errorf("invalid preview workspace")
	}
	return filepath.Join(c.DataDir, "previews", id), nil
}

func removePreviewEntry(ctx context.Context, tx pgx.Tx, c Config, id string) error {
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
	_, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=$1 AND kind IN ('preview','preview_cache')", id)
	return err
}

// Expired requests cannot retain disk space or pin sources indefinitely. Cache
// files are removed before their charge is released, under the admission lock.
func (s *Store) cleanupPreviews(ctx context.Context, c Config) error {
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	rows, err := tx.Query(ctx, "SELECT id FROM storage_reservations WHERE kind='preview' AND expires_at<clock_timestamp() LIMIT $1", storageBatch)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = removePreviewEntry(ctx, tx, c, id); err != nil {
			return err
		}
	}
	if err = prunePreviewCache(ctx, tx, c, "", 0, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func deleteSourcePreviews(ctx context.Context, tx pgx.Tx, c Config, sourceID string) error {
	rows, err := tx.Query(ctx, "SELECT id FROM storage_reservations WHERE kind='preview_cache' AND job_id=$1", sourceID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = removePreviewEntry(ctx, tx, c, id); err != nil {
			return err
		}
	}
	return nil
}
