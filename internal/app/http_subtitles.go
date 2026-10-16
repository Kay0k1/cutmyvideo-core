package app

import (
	"errors"
	"net/http"
)

func (s *Server) retimeSubtitles(w http.ResponseWriter, r *http.Request, _ string) {
	var request SubtitleRetimeRequest
	if err := decodeJSONLimit(w, r, &request, 4<<20); err != nil {
		return
	}
	result, err := RetimeSubtitles(r.Context(), request)
	if err != nil {
		var detail *SubtitleRetimeError
		if errors.As(err, &detail) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": detail.Code, "field": detail.Field, "message": "Check supplied subtitle text and actual segment maps"}})
		} else {
			writeError(w, http.StatusRequestTimeout, "request_timeout", "Subtitle transformation was cancelled")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
