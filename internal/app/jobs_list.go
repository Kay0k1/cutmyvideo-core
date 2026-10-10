package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// History keeps background exports reachable after opening another source.
// Return a bounded summary and only artifacts that still exist, in one query.
type jobSummary struct {
	ID        string     `json:"id"`
	SourceID  string     `json:"source_id"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Stage     string     `json:"stage"`
	CreatedAt time.Time  `json:"created_at"`
	Total     int        `json:"total"`
	Files     []Artifact `json:"files"`
}

func (s *Store) Jobs(ctx context.Context, owner string) ([]jobSummary, error) {
	rows, err := s.DB.Query(ctx, `SELECT j.id,j.source_id,COALESCE(s.title,'Video'),j.status,j.stage,j.created_at,jsonb_array_length(j.items),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'filename',a.filename,'size_bytes',a.size_bytes,
 'actual_start_ms',a.actual_start_ms,'actual_end_ms',a.actual_end_ms,
 'expires_at',CASE WHEN j.status IN ('queued','running','waiting_storage') THEN NULL ELSE a.expires_at END) ORDER BY a.created_at,a.id)
 FROM artifacts a WHERE a.job_id=j.id AND a.owner=$1),'[]'::jsonb)
 FROM jobs j LEFT JOIN sources s ON s.id=j.source_id AND s.owner=$1
 WHERE j.owner=$1 ORDER BY (j.status IN ('queued','running','waiting_storage')) DESC,j.created_at DESC,j.id DESC LIMIT 30`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]jobSummary, 0)
	for rows.Next() {
		var j jobSummary
		var files []byte
		if err = rows.Scan(&j.ID, &j.SourceID, &j.Title, &j.Status, &j.Stage, &j.CreatedAt, &j.Total, &files); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(files, &j.Files); err != nil {
			return nil, err
		}
		for i := range j.Files {
			j.Files[i].DownloadURL = "/api/v1/artifacts/" + j.Files[i].ID + "/download"
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, owner string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	jobs, err := s.Store.Jobs(ctx, owner)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}
