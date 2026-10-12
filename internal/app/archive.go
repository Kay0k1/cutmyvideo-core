package app

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxArchiveFiles = 128

type archiveLocation struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Name  string `json:"name"`
	Label string `json:"label"`
	Size  int64  `json:"size"`
}

// Explicit IDs let a partial retry include the earlier successful files. The
// anchor job, every artifact and its parent must belong to the same source/owner.
func (s *Store) archiveLocations(ctx context.Context, jobID, owner string, ids []string) ([]archiveLocation, error) {
	j, err := s.Job(ctx, jobID, owner)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		for _, item := range j.Items {
			if item.Artifact != nil {
				ids = append(ids, item.Artifact.ID)
			}
		}
	}
	if len(ids) == 0 || len(ids) > maxArchiveFiles {
		return nil, ErrNotFound
	}
	var raw []byte
	err = s.DB.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object(
 'id',a.id,'path',a.path,'name',a.filename,'size',a.size_bytes,
 'label',COALESCE((SELECT item->>'label' FROM jsonb_array_elements(j.items) item WHERE item->'artifact'->>'id'=a.id LIMIT 1),''))), '[]'::jsonb)
 FROM artifacts a JOIN jobs j ON j.id=a.job_id
 WHERE a.id=ANY($1::text[]) AND a.owner=$2 AND j.owner=$2 AND j.source_id=$3
 AND (a.expires_at IS NULL OR a.expires_at>clock_timestamp() OR j.status IN ('queued','running','waiting_storage'))`, ids, owner, j.Request.SourceID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var found []archiveLocation
	if err = json.Unmarshal(raw, &found); err != nil {
		return nil, err
	}
	byID := make(map[string]archiveLocation, len(found))
	for _, file := range found {
		byID[file.ID] = file
	}
	ordered := make([]archiveLocation, 0, len(ids))
	for _, id := range ids {
		file, ok := byID[id]
		if !ok {
			return nil, ErrNotFound
		}
		ordered = append(ordered, file)
	}
	return ordered, nil
}

func (s *Server) downloadArchive(w http.ResponseWriter, r *http.Request, owner string) {
	ids := r.URL.Query()["artifact_id"]
	seen := make(map[string]bool, len(ids))
	if len(ids) > maxArchiveFiles {
		writeError(w, 400, "invalid_archive", "Choose at most 128 ready files")
		return
	}
	for _, id := range ids {
		if id == "" || len(id) > 160 || seen[id] {
			writeError(w, 400, "invalid_archive", "Choose distinct ready file IDs")
			return
		}
		seen[id] = true
	}
	files, err := apiDatabase(r.Context(), func(ctx context.Context) ([]archiveLocation, error) {
		return s.Store.archiveLocations(ctx, r.PathValue("id"), owner, ids)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	serveArchive(w, r, files)
}

func archiveEntryName(index int, filename string) string {
	name := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." {
		name = "clip"
	}
	for len(name) > 180 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	// Ordinals both preserve the chosen sequence and make duplicate names safe.
	number := strconv.Itoa(index + 1)
	return strings.Repeat("0", max(0, 3-len(number))) + number + "-" + name
}

func serveArchive(w http.ResponseWriter, r *http.Request, locations []archiveLocation) {
	opened := make([]*os.File, 0, len(locations))
	defer func() {
		for _, file := range opened {
			_ = file.Close()
		}
	}()
	infos := make([]os.FileInfo, 0, len(locations))
	// Open all files before writing headers. Existing descriptors survive normal
	// Unix retention unlink; an already missing file yields a clean JSON error.
	for _, location := range locations {
		if r.Context().Err() != nil {
			return
		}
		file, err := os.Open(location.Path)
		if err != nil {
			writeError(w, 404, "expired", "A file has expired or is no longer available")
			return
		}
		opened = append(opened, file)
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != location.Size {
			writeError(w, 404, "expired", "A file is no longer available")
			return
		}
		infos = append(infos, info)
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(12 * time.Hour))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "cutmy-clips.zip"}))
	if r.Method == http.MethodHead {
		return
	}
	writer := zip.NewWriter(w)
	buffer := make([]byte, 64<<10)
	for i, file := range opened {
		name := locations[i].Name
		if strings.TrimSpace(locations[i].Label) != "" {
			name = locations[i].Label
			ext := path.Ext(locations[i].Name)
			if !strings.EqualFold(path.Ext(name), ext) {
				name += ext
			}
		}
		header := &zip.FileHeader{Name: archiveEntryName(i, name), Method: zip.Store, Modified: infos[i].ModTime()}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		if _, err = io.CopyBuffer(entry, contextReadSeeker{r.Context(), file}, buffer); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
	if err := writer.Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
}
