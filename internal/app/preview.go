package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const previewWindowMS int64 = 30000
const previewOutputBytes int64 = 16 << 20
const previewInputBytes int64 = 64 << 20
const previewReservationBytes = 2*previewInputBytes + previewOutputBytes
const previewTimeout = 90 * time.Second
const previewReservationTTL = 5 * time.Minute

// Previews retain only one bounded interval. No full recording or second
// persistent copy is needed, including for HLS recordings many hours long.
func (s *Server) sourcePreview(w http.ResponseWriter, r *http.Request, owner string) {
	ctx, cancel := context.WithTimeout(r.Context(), previewTimeout)
	defer cancel()
	v, err := s.Store.Source(ctx, r.PathValue("id"), owner)
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
	id := newID("preview")
	if err = s.Store.reservePreview(ctx, s.Config, v, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			lookupError(w, err)
			return
		}
		writeSourceAdmissionError(w, ctx, err)
		return
	}
	dir := filepath.Join(s.Config.DataDir, "previews", id)
	defer func() {
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
	rangeMS := Range{StartMS: start, EndMS: min(start+previewWindowMS, v.DurationMS)}
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
	// Never cache a temporary URL or expose private upstream addresses.
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("X-Preview-Start-MS", strconv.FormatInt(start, 10))
	w.Header().Set("X-Preview-End-MS", strconv.FormatInt(rangeMS.EndMS, 10))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	http.ServeFile(w, r, out)
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
	tx, err := s.storageTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if err = s.bootstrapStorage(ctx, tx, c); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sources WHERE id=$1 AND owner=$2)", source.ID, source.Owner).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	var busy bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE kind='preview')").Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errSourceServerBusy
	}
	if c.MaxStorageBytes > 0 {
		fits, err := storageFits(ctx, tx, c, previewReservationBytes, "")
		if err != nil {
			return err
		}
		if !fits {
			return errSourceStorage
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES($1,$2,'preview',$3,clock_timestamp()+($4*interval '1 second'),$5)`, id, source.Owner, previewReservationBytes, previewReservationTTL.Seconds(), source.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Expired requests cannot keep disk space or hold source deletion indefinitely.
// Directory removal happens before releasing the charged reservation, under
// the same lock as admissions. An interrupted removal is safely retried.
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
		if filepath.Base(id) != id {
			return fmt.Errorf("invalid preview workspace")
		}
		if err = removeStorageTree(ctx, filepath.Join(c.DataDir, "previews", id)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM storage_reservations WHERE id=ANY($1) AND kind='preview'", ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
