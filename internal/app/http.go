package app

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type Server struct {
	Config                Config
	Store                 *Store
	mu                    sync.Mutex
	preparing             map[string]bool
	preparationTokens     map[string]string
	preparationSources    map[string]string
	uncertainPreparations map[string]bool
	rate                  map[string]*rateEntry
	rateCleanup           time.Time
	slots                 chan struct{}
}

func NewServer(c Config, s *Store) *Server {
	if s != nil {
		s.ConfigureStorage(c)
	}
	return &Server{Config: c, Store: s, preparing: map[string]bool{}, preparationTokens: map[string]string{}, preparationSources: map[string]string{}, uncertainPreparations: map[string]bool{}, rate: map[string]*rateEntry{}, rateCleanup: time.Now().Add(time.Minute), slots: make(chan struct{}, 4)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.Store.DB.Ping(ctx); err != nil {
			writeError(w, 503, "not_ready", "Database is unavailable")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/session", s.session)
	s.registerProjectRoutes(mux)
	mux.HandleFunc("GET /api/v1/sources", s.withSession(s.listSources))
	mux.HandleFunc("POST /api/v1/sources", s.withSession(s.addSource))
	mux.HandleFunc("POST /api/v1/uploads", s.withSession(s.upload))
	mux.HandleFunc("GET /api/v1/source-preparations/{key}", s.withSession(s.sourcePreparation))
	mux.HandleFunc("POST /api/v1/subtitles/retime", s.withSession(s.retimeSubtitles))
	mux.HandleFunc("POST /api/v1/cut-lists/import", s.withSession(s.importCutList))
	mux.HandleFunc("POST /api/v1/cut-lists/export", s.withSession(s.exportCutList))
	mux.HandleFunc("DELETE /api/v1/sources/{id}", s.withSession(s.deleteSource))
	mux.HandleFunc("GET /api/v1/sources/{id}", s.withSession(s.source))
	mux.HandleFunc("GET /api/v1/sources/{id}/media", s.withSession(s.sourceMedia))
	mux.HandleFunc("GET /api/v1/sources/{id}/preview", s.withSession(s.sourcePreview))
	mux.HandleFunc("GET /api/v1/sources/{id}/storyboard", s.withSession(s.sourceStoryboard))
	mux.HandleFunc("GET /api/v1/sources/{id}/waveform", s.withSession(s.sourceWaveform))
	mux.HandleFunc("GET /api/v1/sources/{id}/tracks", s.withSession(s.sourceTracks))
	mux.HandleFunc("POST /api/v1/sources/{id}/analysis/silence", s.withSession(s.analyzeSourceSilence))
	mux.HandleFunc("POST /api/v1/sources/{id}/analysis/video", s.withSession(s.analyzeSourceVideo))
	mux.HandleFunc("POST /api/v1/sources/{id}/analysis/loudness", s.withSession(s.analyzeSourceLoudness))
	mux.HandleFunc("GET /api/v1/sources/{id}/thumbnail", s.withSession(s.sourceThumbnail))
	mux.HandleFunc("POST /api/v1/jobs", s.withSession(s.createJob))
	mux.HandleFunc("POST /api/v1/jobs/preflight", s.withSession(s.preflightJob))
	mux.HandleFunc("POST /api/v1/collections/validate", s.withSession(s.validateCollection))
	mux.HandleFunc("POST /api/v1/collections", s.withSession(s.submitCollection))
	mux.HandleFunc("GET /api/v1/jobs", s.withSession(s.listJobs))
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.withSession(s.job))
	mux.HandleFunc("GET /api/v1/jobs/{id}/download.zip", s.withSession(s.downloadArchive))
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.withSession(s.cancelJob))
	mux.HandleFunc("GET /api/v1/artifacts/{id}/download", s.withSession(s.download))
	mux.HandleFunc("POST /api/v1/artifacts/{id}/poster", s.withSession(s.createPoster))
	mux.HandleFunc("GET /api/v1/image-artifacts/{id}", s.withSession(s.getImageArtifact))
	mux.HandleFunc("GET /api/v1/image-artifacts/{id}/download", s.withSession(s.downloadImageArtifact))
	mux.HandleFunc("DELETE /api/v1/image-artifacts/{id}", s.withSession(s.deleteImageArtifact))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Body != nil && r.Body != http.NoBody {
			body := &trackedRequestBody{ReadCloser: r.Body}
			r.Body = body
			w = &bodyGuardResponseWriter{ResponseWriter: w, body: body, http1: r.ProtoMajor == 1}
			defer func() {
				if !body.complete {
					_ = http.NewResponseController(w).SetReadDeadline(time.Now())
				}
			}()
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				if r.ProtoMajor == 1 {
					w.Header().Set("Connection", "close")
				}
				writeError(w, 400, "invalid_request", "This endpoint does not accept a request body")
				return
			}
		}
		if (r.Method == "POST" || r.Method == "DELETE" || r.Method == "PUT" || r.Method == "PATCH") && !s.validOrigin(r) {
			writeError(w, 403, "origin_rejected", "This request must come from the same site")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
