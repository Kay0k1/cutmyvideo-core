package app

import (
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
)

const maxThumbnailBytes int64 = 2 << 20

// Only locally staged, inspected images are shown by clients. Signed extractor
// addresses never become visible URLs; the source owner guards the endpoint.
func fetchThumbnail(ctx context.Context, c Config, id, raw string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	u, err := validateURL(raw)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	client := safeClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("thumbnail unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxThumbnailBytes {
		return "", errors.New("thumbnail unavailable")
	}
	return stageThumbnail(ctx, c, id, resp.Body, fsdurable.Publish)
}

func stageThumbnail(ctx context.Context, c Config, id string, body io.Reader, publish func(context.Context, string, string) (os.FileInfo, error)) (string, error) {
	dir := filepath.Join(c.DataDir, "sources")
	if err := fsdurable.EnsureDirectory(dir, 0700); err != nil {
		return "", err
	}
	if err := fsdurable.Preflight(dir, dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".thumbnail")
	stage := path + ".part"
	f, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	keep := false
	linked := false
	defer func() {
		f.Close()
		_ = removeSourcePreparation(stage)
		if !keep {
			if linked {
				_ = removeSourcePreparation(path)
			}
		}
	}()
	if err = copyBounded(f, body, maxThumbnailBytes); err != nil {
		return "", errors.New("thumbnail exceeds limit")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	config, format, err := image.DecodeConfig(f)
	if err != nil || (format != "jpeg" && format != "png") || config.Width <= 0 || config.Height <= 0 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 20_000_000 {
		return "", errors.New("unsupported thumbnail")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	// DecodeConfig alone accepts a truncated image with an intact header.
	// Decode only after the dimension bound limits allocation.
	if _, _, err = image.Decode(f); err != nil {
		return "", errors.New("unsupported thumbnail")
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	_, err = publish(ctx, stage, path)
	linked = err == nil || errors.Is(err, fsdurable.ErrPublicationUncertain)
	if err != nil {
		return "", err
	}
	if err = removeSourcePreparation(stage); err != nil {
		return "", err
	}
	keep = true
	return path, nil
}
func (s *Server) sourceThumbnail(w http.ResponseWriter, r *http.Request, owner string) {
	v, err := s.databaseSource(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if v.ThumbnailPath == "" {
		writeError(w, 404, "preview_unavailable", "A thumbnail is not available for this source")
		return
	}
	f, err := os.Open(v.ThumbnailPath)
	if err != nil {
		writeError(w, 404, "not_found", "The thumbnail has expired")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		internalError(w, err)
		return
	}
	bytes := make([]byte, 512)
	n, _ := f.Read(bytes)
	mime := http.DetectContentType(bytes[:n])
	if mime != "image/jpeg" && mime != "image/png" {
		writeError(w, 404, "preview_unavailable", "A thumbnail is not available for this source")
		return
	}
	_, _ = f.Seek(0, io.SeekStart)
	w.Header().Set("Content-Type", mime)
	privateSourceFileHeaders(w, v.ID, info)
	http.ServeContent(w, r, "thumbnail", info.ModTime(), f)
}
