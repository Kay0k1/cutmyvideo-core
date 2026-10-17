package app

import (
	"net/http"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

type CutListImportRequest struct {
	Format string `json:"format"`
	Text   string `json:"text"`
}

// Export accepts portable decisions or actual output timestamps. Paths, media
// references and arbitrary timeline settings are intentionally not accepted.
type CutListExportRequest struct {
	Format   string          `json:"format"`
	CutList  *edit.CutList   `json:"cut_list,omitempty"`
	Segments *[]edit.Segment `json:"segments,omitempty"`
}

type CutListText struct {
	Version int    `json:"version"`
	Format  string `json:"format"`
	Text    string `json:"text"`
}

func (s *Server) importCutList(w http.ResponseWriter, r *http.Request, owner string) {
	var request *CutListImportRequest
	if err := decodeJSONLimit(w, r, &request, 2<<20); err != nil {
		return
	}
	if request == nil {
		writeError(w, 400, "invalid_cut_list", "Send format and text")
		return
	}
	if !s.allow("cutlist:"+owner, 60) {
		writeError(w, 429, "rate_limit", "Wait before converting another cut list")
		return
	}
	list, err := edit.ParseCutList(request.Format, []byte(request.Text))
	if err != nil {
		writeError(w, 400, "invalid_cut_list", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) exportCutList(w http.ResponseWriter, r *http.Request, owner string) {
	var request *CutListExportRequest
	if err := decodeJSONLimit(w, r, &request, 1<<20); err != nil {
		return
	}
	if request == nil || (request.CutList == nil) == (request.Segments == nil) {
		writeError(w, 400, "invalid_cut_list", "Choose exactly one cut_list or actual segments")
		return
	}
	if !s.allow("cutlist:"+owner, 60) {
		writeError(w, 429, "rate_limit", "Wait before converting another cut list")
		return
	}
	var list edit.CutList
	var err error
	if request.CutList != nil {
		list = *request.CutList
	} else {
		list, err = edit.CutListFromSegments(*request.Segments)
	}
	if err != nil {
		writeError(w, 400, "invalid_cut_list", err.Error())
		return
	}
	data, err := edit.EncodeCutList(request.Format, list)
	if err != nil {
		writeError(w, 400, "invalid_cut_list", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, CutListText{Version: 1, Format: request.Format, Text: string(data)})
}
