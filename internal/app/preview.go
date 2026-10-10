package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const previewWindowMS int64 = 30000
const previewOutputBytes int64 = 16 << 20
const previewInputBytes int64 = 64 << 20
const previewReservationBytes = 2*previewInputBytes + previewOutputBytes
const previewTimeout = 90 * time.Second
const previewReservationTTL = 5 * time.Minute
const previewCacheTTL = 30 * time.Minute
const previewCacheBytes int64 = 512 << 20
const previewOwnerCacheBytes int64 = 128 << 20
const previewCacheEntries = 64
const previewOwnerCacheEntries = 16

var errPreviewPending = errors.New("this preview is being prepared")

// Small, reusable intervals keep seeking independent of recording length.
// Cached files remain private and charged to the durable storage ledger.
func (s *Server) sourcePreview(w http.ResponseWriter, r *http.Request, owner string) {
	ctx, cancel := context.WithTimeout(r.Context(), previewTimeout)
	defer cancel()
	v, err := s.databaseSource(ctx, r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	start, err := strconv.ParseInt(r.URL.Query().Get("start_ms"), 10, 64)
	if err != nil || start < 0 || start >= v.DurationMS {
		writeError(w, 400, "invalid_preview", "Choose a position inside the video")
		return
	}
	if !s.allow("preview:"+owner, 60) {
		writeError(w, 429, "rate_limit", "Wait before requesting another preview")
		return
	}
	// Canonical intervals make nearby seeks reuse the same result.
	start = start / previewWindowMS * previewWindowMS
	key := previewCacheKey(v, start)
	id := newID("preview")
	var cached *os.File
	for {
		cached, err = apiDatabase(ctx, func(dbCtx context.Context) (*os.File, error) {
			return s.Store.acquirePreview(dbCtx, s.Config, v, id, key, r.URL.Query().Get("priority") == "background")
		})
		if !errors.Is(err, errPreviewPending) {
			break
		}
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				writeError(w, 504, "source_timeout", "Preview preparation timed out; try again")
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			lookupError(w, err)
			return
		}
		writeSourceAdmissionError(w, ctx, err)
		return
	}
	rangeMS := Range{StartMS: start, EndMS: min(start+previewWindowMS, v.DurationMS)}
	if cached != nil {
		defer cached.Close()
		servePreview(w, r, cached, rangeMS, "hit")
		return
	}
	dir := filepath.Join(s.Config.DataDir, "previews", id)
	retain := false
	defer func() {
		if retain {
			return
		}
		cleanupCtx, stop := context.WithTimeout(context.Background(), workerDatabaseTimeout)
		defer stop()
		// Keep the reservation if removal failed; maintenance retries it.
		if removeStorageTree(cleanupCtx, dir) == nil {
			_, _ = s.Store.DB.Exec(cleanupCtx, "DELETE FROM storage_reservations WHERE id=$1 AND kind='preview'", id)
		}
	}()
	if err = os.MkdirAll(dir, 0700); err != nil {
		internalError(w, err)
		return
	}
	c := s.Config
	c.MaxSourceBytes, c.MaxFetchBytes, c.MaxOutputBytes = previewInputBytes, previewInputBytes, previewOutputBytes
	out := filepath.Join(dir, "preview.mp4")
	inputs := []mediaInput{{Path: v.Path}}
	if v.Path == "" {
		guard, e := newNetworkGuard(previewInputBytes)
		if e != nil {
			internalError(w, e)
			return
		}
		defer guard.Close()
		render := func(fresh bool) error {
			request := ExportRequest{Quality: "480p", Format: "mp4", CutMode: "accurate"}
			resolved, streams, playlists, _, e := platformInputs(ctx, c, s.Store, v, guard, request, fresh)
			if e != nil {
				return e
			}
			for i, stream := range streams {
				if !isHLS(stream) {
					continue
				}
				// A retry uses a separate filename and the same transfer budget.
				index := i
				if fresh {
					index += 10
				}
				path, offset, e := stageHLS(ctx, c, guard, stream, playlists[i], rangeMS, dir, index)
				if e != nil {
					return e
				}
				resolved[i] = mediaInput{Path: path, OffsetMS: offset}
			}
			return renderPreview(ctx, c, resolved, rangeMS, out)
		}
		err = render(false)
		if cachedAddressDenied(err) && ctx.Err() == nil {
			err = render(true)
		}
	} else {
		err = renderPreview(ctx, c, inputs, rangeMS, out)
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		problem := exportProblem(err)
		writeError(w, 422, problem.code, problem.message)
		return
	}
	// Release staged inputs before turning the processing reservation into a
	// cache entry charged at the actual MP4 size. Keep a file descriptor open:
	// eviction/source deletion can unlink the file without interrupting a read.
	file, err := os.Open(out)
	if err != nil {
		internalError(w, err)
		return
	}
	defer file.Close()
	if err = trimPreviewWorkspace(ctx, dir); err != nil {
		internalError(w, err)
		return
	}
	// A failed COMMIT can have succeeded remotely. Preserve the bounded lease
	// until maintenance decides its state instead of deleting a published file.
	retain = true
	if err = apiDatabaseExec(ctx, func(dbCtx context.Context) error {
		return s.Store.publishPreview(dbCtx, s.Config, v, id, file)
	}); err != nil {
		internalError(w, err)
		return
	}
	servePreview(w, r, file, rangeMS, "miss")
}

func previewCacheKey(source Source, start int64) string {
	sum := sha256.Sum256([]byte(source.ID + ":" + strconv.FormatInt(start, 10)))
	return hex.EncodeToString(sum[:])
}

func servePreview(w http.ResponseWriter, r *http.Request, file *os.File, interval Range, cache string) {
	info, err := file.Stat()
	if err != nil {
		internalError(w, err)
		return
	}
	// The browser's bounded blob cache is session-local. HTTP/shared caches
	// must never bypass source ownership checks or retain private URLs.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("X-Preview-Cache", cache)
	w.Header().Set("X-Preview-Start-MS", strconv.FormatInt(interval.StartMS, 10))
	w.Header().Set("X-Preview-End-MS", strconv.FormatInt(interval.EndMS, 10))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	http.ServeContent(w, r, "preview.mp4", info.ModTime(), file)
}

func trimPreviewWorkspace(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "preview.mp4" {
			if err := removeStorageTree(ctx, filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderPreview(ctx context.Context, c Config, inputs []mediaInput, r Range, out string) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-filter_threads", "1"}
	for _, input := range inputs {
		if r.StartMS < input.OffsetMS {
			return errUnsupportedStream
		}
		protocols := "file"
		if input.Remote {
			protocols = "http,tcp"
		}
		args = append(args, "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats)
		args = append(args, mediaInputBounds(c)...)
		args = append(args, "-ss", seconds(r.StartMS-input.OffsetMS), "-i", input.Path)
	}
	args = append(args, "-t", seconds(r.EndMS-r.StartMS), "-map", "0:v:0")
	if len(inputs) > 1 {
		args = append(args, "-map", "1:a:0")
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-vf", "scale=w='min(854,iw)':h='min(480,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,fps=24", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "28", "-maxrate", "1400k", "-bufsize", "2800k", "-pix_fmt", "yuv420p", "-threads", "2", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(previewOutputBytes, 10), out)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return err
	}
	stat, err := os.Stat(out)
	if err != nil {
		return err
	}
	if stat.Size() >= previewOutputBytes {
		return errOutputLimit
	}
	_, duration, err := probe(ctx, c, out, false)
	if err != nil {
		return err
	}
	if delta := duration - (r.EndMS - r.StartMS); delta > 350 || delta < -350 {
		return errors.New("preview duration does not match requested interval")
	}
	return nil
}

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
	tx, err := s.storageTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackStorage(tx)
	if err = s.bootstrapStorage(ctx, tx, c); err != nil {
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
		file, openErr := os.Open(filepath.Join(path, "preview.mp4"))
		if openErr == nil {
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() >= previewOutputBytes {
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
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN s.width>0 AND s.height>0 AND s.width<=8388608/GREATEST(1,s.height) THEN 1 ELSE 2 END),0),COALESCE(bool_or(r.job_id=$1 AND r.token=$2),false) FROM storage_reservations r LEFT JOIN sources s ON s.id=r.job_id WHERE r.kind='preview'`, source.ID, key).Scan(&active, &weight, &pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, errPreviewPending
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
	return nil, tx.Commit(ctx)
}

func (s *Store) publishPreview(ctx context.Context, c Config, source Source, id string, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if err = prunePreviewCache(ctx, tx, c, source.Owner, info.Size(), false); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE storage_reservations SET kind='preview_cache',size_bytes=$2,expires_at=clock_timestamp()+($3*interval '1 second') WHERE id=$1 AND kind='preview' AND expires_at>clock_timestamp()", id, info.Size(), previewCacheTTL.Seconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("preview lease expired")
	}
	return tx.Commit(ctx)
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
