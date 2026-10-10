package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
type rateEntry struct {
	count int
	reset time.Time
}

const maxRateEntries = 10000
const requestsPerIPPerMinute = 1200
const jsonReadTimeout = 10 * time.Second

var (
	errSourceLimit      = &sourceProblem{"source_limit", "This session has reached its source limit; wait for older sources to expire"}
	errSourceBusy       = &sourceProblem{"source_busy", "Wait for the current source preparation or cancel it before opening another"}
	errSourceStorage    = &sourceProblem{"storage_limit", "Source storage is full; use a smaller file or wait for older files to expire"}
	errSourceServerBusy = &sourceProblem{"server_busy", "The server is preparing other sources; try again shortly"}
)

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
	mux.HandleFunc("GET /api/v1/sources", s.withSession(s.listSources))
	mux.HandleFunc("POST /api/v1/sources", s.withSession(s.addSource))
	mux.HandleFunc("POST /api/v1/uploads", s.withSession(s.upload))
	mux.HandleFunc("DELETE /api/v1/sources/{id}", s.withSession(s.deleteSource))
	mux.HandleFunc("GET /api/v1/sources/{id}", s.withSession(s.source))
	mux.HandleFunc("GET /api/v1/sources/{id}/media", s.withSession(s.sourceMedia))
	mux.HandleFunc("GET /api/v1/sources/{id}/preview", s.withSession(s.sourcePreview))
	mux.HandleFunc("GET /api/v1/sources/{id}/thumbnail", s.withSession(s.sourceThumbnail))
	mux.HandleFunc("POST /api/v1/jobs", s.withSession(s.createJob))
	mux.HandleFunc("GET /api/v1/jobs", s.withSession(s.listJobs))
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.withSession(s.job))
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.withSession(s.cancelJob))
	mux.HandleFunc("GET /api/v1/artifacts/{id}/download", s.withSession(s.download))
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
		if (r.Method == "POST" || r.Method == "DELETE") && !s.validOrigin(r) {
			writeError(w, 403, "origin_rejected", "This request must come from the same site")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) validOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if s.Config.PublicOrigin != "" {
		return origin == strings.TrimSuffix(s.Config.PublicOrigin, "/")
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}

func ownerFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie("cutmy_session")
	if err != nil {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(b) != 32 {
		return "", false
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), true
}

func (s *Server) allow(key string, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// Bound cleanup work under the shared mutex even when the table is full.
	// Previously every denied request scanned all entries and a full table also
	// rejected existing clients regardless of their own remaining allowance.
	if !now.Before(s.rateCleanup) {
		for k, v := range s.rate {
			if !now.Before(v.reset) {
				delete(s.rate, k)
			}
		}
		s.rateCleanup = now.Add(time.Second)
	}
	v := s.rate[key]
	if v == nil || !now.Before(v.reset) {
		if v == nil && len(s.rate) >= maxRateEntries {
			return false
		}
		v = &rateEntry{reset: now.Add(time.Minute)}
		s.rate[key] = v
	}
	if v.count >= limit {
		return false
	}
	v.count++
	return true
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.allow("session:"+ip, 60) {
		writeError(w, 429, "rate_limit", "Too many requests; wait a minute")
		return
	}
	if _, ok := ownerFromRequest(r); !ok {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			writeError(w, 500, "internal", "Could not create a session")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(b), Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true, Secure: s.Config.SecureCookie, SameSite: http.SameSiteLaxMode})
	}
	writeJSON(w, 200, map[string]any{"ok": true, "limits": map[string]any{"max_source_bytes": s.Config.MaxSourceBytes, "max_output_bytes": s.Config.MaxOutputBytes, "max_fetch_bytes": remoteSourceBudget(s.Config), "max_ranges": s.Config.MaxRanges, "max_range_ms": s.Config.MaxRangeMS, "max_job_ms": s.Config.MaxJobMS}})
}

type ownerHandler func(http.ResponseWriter, *http.Request, string)

func (s *Server) clientIP(r *http.Request) string {
	if s.Config.TrustProxy {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[len(parts)-1])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func (s *Server) withSession(next ownerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := ownerFromRequest(r)
		if !ok {
			writeError(w, 401, "session_required", "Create a session before using this endpoint")
			return
		}
		// Apply IP admission before allocating a per-owner entry: capability
		// cookies are deliberately client-generatable and may be rotated freely.
		ip := s.clientIP(r)
		if !s.allow("requests-ip:"+ip, requestsPerIPPerMinute) {
			writeError(w, 429, "rate_limit", "Too many requests; wait a minute")
			return
		}
		if (r.Method == "POST" || r.Method == "DELETE") && !s.allow("mutations:"+ip, s.Config.MutationsPerMinute) {
			writeError(w, 429, "rate_limit", "Too many changes; wait a minute")
			return
		}
		if !s.allow("requests:"+owner, 180) {
			writeError(w, 429, "rate_limit", "Too many requests; wait a minute")
			return
		}
		next(w, r, owner)
	}
}

func (s *Server) beginSource(ctx context.Context, owner string, thumbnails ...bool) error {
	return s.beginSourceWithBytes(ctx, owner, s.Config.MaxSourceBytes, thumbnails...)
}

func (s *Server) beginSourceWithBytes(ctx context.Context, owner string, bytes int64, thumbnails ...bool) error {
	// Reserve the owner's preparation slot before reading quota state. A second
	// request must not carry an old count/size past the first request's completion.
	s.mu.Lock()
	if s.preparing[owner] {
		s.mu.Unlock()
		return errSourceBusy
	}
	select {
	case s.slots <- struct{}{}:
		s.preparing[owner] = true
		s.preparationTokens[owner] = newID("prep")
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		return errSourceServerBusy
	}
	accepted := false
	defer func() {
		if !accepted {
			s.endSource(owner)
		}
	}()
	if err := apiDatabaseExec(ctx, func(dbCtx context.Context) error {
		return s.Store.reserveSource(dbCtx, s.Config, owner, s.sourceToken(owner), len(thumbnails) > 0 && thumbnails[0], bytes)
	}); err != nil {
		return err
	}
	accepted = true
	return nil
}

func sourceTimedOut(ctx context.Context, err error) bool {
	var networkError net.Error
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout()
}

func writeSourceAdmissionError(w http.ResponseWriter, ctx context.Context, err error) {
	if errors.Is(err, errDatabaseUnavailable) {
		internalError(w, err)
	} else if sourceTimedOut(ctx, err) {
		writeError(w, 504, "source_timeout", "Source preparation timed out; try again")
	} else if problem := problemFromError(err); problem != nil {
		writeError(w, 429, problem.code, problem.message)
	} else {
		internalError(w, err)
	}
}
func (s *Server) endSource(owner string) {
	ctx, cancel := context.WithTimeout(context.Background(), workerDatabaseTimeout)
	defer cancel()
	s.mu.Lock()
	held := s.uncertainPreparations[owner]
	s.mu.Unlock()
	if err := func() error {
		if held {
			return nil
		}
		return s.Store.FinishSource(ctx, s.Config, owner, s.sourceToken(owner), s.sourceIdentity(owner, ""))
	}(); err != nil {
		slog.Error("source reservation release failed", "error", err)
	}
	s.mu.Lock()
	delete(s.preparing, owner)
	delete(s.preparationTokens, owner)
	delete(s.preparationSources, owner)
	delete(s.uncertainPreparations, owner)
	s.mu.Unlock()
	<-s.slots
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request, owner string) {
	s.addSourceWithClient(w, r, owner, safeClient)
}

func (s *Server) addSourceWithClient(w http.ResponseWriter, r *http.Request, owner string, sourceClient func() *http.Client) {
	var body struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if len(body.URL) > 8192 {
		writeError(w, 400, "invalid_url", "The link is too long")
		return
	}
	u, err := normalizeSourceURL(body.URL)
	if err != nil {
		if errors.Is(err, errInvalidYouTubeURL) {
			writeError(w, 400, "invalid_youtube_url", "Paste a valid YouTube video link without credentials or a custom port")
			return
		}
		if p := problemFromError(err); p != nil {
			status := 400
			if p.code == "live_not_supported" {
				status = 422
			}
			writeError(w, status, p.code, p.message)
			return
		}
		writeError(w, 400, "invalid_url", "Use a public HTTPS link without credentials or a custom port")
		return
	}
	if len(u.String()) > 8192 {
		writeError(w, 400, "invalid_url", "The link is too long")
		return
	}
	timeout := s.Config.SourceTimeout
	if !isPlatformHost(u.Hostname()) {
		timeout = max(timeout, uploadTimeout(s.Config))
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + time.Minute))
	if err = s.beginSource(ctx, owner, isPlatformHost(u.Hostname())); err != nil {
		writeSourceAdmissionError(w, ctx, err)
		return
	}
	defer s.endSource(owner)
	v := Source{ID: newID("src"), Owner: owner, StorageToken: s.sourceToken(owner), URL: u.String(), Kind: "direct"}
	s.sourceIdentity(owner, v.ID)
	platform := isPlatformHost(u.Hostname())
	if !platform {
		req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		req.Header.Set("User-Agent", "cutmy-core/0.1")
		client := sourceClient()
		defer client.CloseIdleConnections()
		resp, e := client.Do(req)
		if e != nil {
			if sourceTimedOut(ctx, e) {
				writeError(w, 504, "source_timeout", "Source download timed out; try again or upload your file")
			} else {
				writeError(w, 422, "source_unavailable", "The source cannot be reached or its address is blocked")
			}
			return
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			writeError(w, 422, "source_unavailable", "The source did not return an accessible file")
			return
		}
		contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if strings.Contains(contentType, "html") {
			resp.Body.Close()
			platform = true
		} else {
			defer resp.Body.Close()
			if resp.ContentLength > s.Config.MaxSourceBytes {
				writeError(w, 413, "source_too_large", "The source exceeds the download limit")
				return
			}
			file, path, e := makeSourceFile(s.Config, v.ID)
			if e != nil {
				internalError(w, e)
				return
			}
			keep := false
			defer func() {
				file.Close()
				if !keep {
					_ = removeSourcePreparation(path, v.Path)
				}
			}()
			if e = copyBounded(file, resp.Body, s.Config.MaxSourceBytes); e != nil {
				if sourceTimedOut(ctx, e) {
					writeError(w, 504, "source_timeout", "Source download timed out; try again or upload your file")
				} else if errors.Is(e, errSourceTooLarge) {
					writeError(w, 413, "source_too_large", "The source exceeds the download limit")
				} else {
					writeError(w, 422, "source_unavailable", "Source download was interrupted; try again or upload your file")
				}
				return
			}
			if e = file.Close(); e != nil {
				internalError(w, e)
				return
			}
			v.Path = path
			v.Title = filepath.Base(u.Path)
			if v.Title == "." || v.Title == "/" || v.Title == "" {
				v.Title = "Video"
			}
			if e = s.completeLocalSource(ctx, &v); e != nil {
				if sourceTimedOut(ctx, e) {
					writeError(w, 504, "source_timeout", "Source inspection timed out; try again or upload your file")
				} else {
					writeError(w, 422, "unsupported_media", "This file has no supported finite video or audio stream")
				}
				return
			}
			if e = publishSourceMedia(ctx, s.Config, &v); e != nil {
				writeSourcePersistenceError(w, ctx, e)
				return
			}
			if e = s.databaseAddSource(ctx, v); e != nil {
				keep = errors.Is(e, ErrSourceCommitUncertain)
				if keep {
					s.holdSourceReservation(owner)
				}
				writeSourcePersistenceError(w, ctx, e)
				return
			}
			keep = true
		}
	}
	if platform {
		g, e := newNetworkGuard(32 << 20)
		if e != nil {
			internalError(w, e)
			return
		}
		defer g.Close()
		info, cached := s.Store.RecentPlatformMetadata(ctx, owner, v.URL)
		if !cached {
			info, e = platformMetadata(ctx, s.Config, v.URL, g)
		}
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) {
				writeError(w, 504, "source_timeout", "Source inspection timed out; try a direct file or upload")
			} else if p := problemFromError(e); p != nil {
				writeError(w, 422, p.code, p.message)
			} else {
				writeError(w, 422, "platform_unavailable", errPlatformUnavailable.message)
			}
			return
		}
		info, durationMS, e := inspectCachedPlatformSource(ctx, g, info, cached, func() (platformInfo, error) {
			s.Store.invalidateRecentPlatformMetadata(ctx, owner, v.URL)
			expected := v
			expected.ProviderID = info.ID
			expected.DurationMS = int64(math.Round(info.Duration * 1000))
			return forceFreshPlatformMetadata(ctx, s.Config, expected, g)
		})
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) {
				writeError(w, 504, "source_timeout", "Source inspection timed out; try a direct file or upload")
			} else if problem := problemFromError(e); problem != nil {
				writeError(w, 422, problem.code, problem.message)
			} else {
				writeError(w, 422, "platform_unavailable", errPlatformUnavailable.message)
			}
			return
		}
		v.Kind = "platform"
		v.Title = info.Title
		v.ProviderID = info.ID
		v.Provider = providerForExtractor(info.Extractor)
		v.DurationMS = durationMS
		if strings.EqualFold(info.Extractor, "Youtube") && validVideoID(info.ID) {
			v.Kind = "youtube"
			embed := "https://www.youtube-nocookie.com/embed/" + info.ID
			v.EmbedURL = &embed
		}
		for _, f := range info.Formats {
			if f.Height > v.Height {
				v.Height = f.Height
				v.Width = f.Width
			}
		}
		v.ThumbnailPath, _ = fetchThumbnail(ctx, s.Config, v.ID, info.Thumbnail)
		completeSourcePresentation(&v)
		if e = s.databaseAddSource(ctx, v); e != nil {
			if errors.Is(e, ErrSourceCommitUncertain) {
				s.holdSourceReservation(owner)
			}
			if v.ThumbnailPath != "" && !errors.Is(e, ErrSourceCommitUncertain) {
				_ = removeSourcePreparation(v.ThumbnailPath)
			}
			writeSourcePersistenceError(w, ctx, e)
			return
		}
		_ = s.Store.CachePlatformMetadata(ctx, v, info)
	}
	if v.Path != "" {
		preview := "/api/v1/sources/" + v.ID + "/media"
		v.PreviewURL = &preview
	}
	completeSourcePresentation(&v)
	if persisted, err := s.databaseSource(ctx, v.ID, owner); err == nil {
		v = persisted
	}
	writeJSON(w, 201, v)
}

func validVideoID(id string) bool {
	if len(id) != 11 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (s *Server) completeLocalSource(ctx context.Context, v *Source) error {
	p, duration, err := probe(ctx, s.Config, v.Path, false)
	if err != nil {
		return err
	}
	v.DurationMS = duration
	for _, stream := range p.Streams {
		if stream.CodecType == "video" {
			v.Width = stream.Width
			v.Height = stream.Height
			break
		}
	}
	return nil
}

func inspectPlatformSource(ctx context.Context, g *networkGuard, info platformInfo) (int64, error) {
	durationMS := int64(math.Round(info.Duration * 1000))
	selected, err := pickStreams(info, "best", "mp4")
	if err != nil {
		selected, err = pickStreams(info, "best", "mp3")
		if err != nil {
			return 0, errUnsupportedStream
		}
	}
	for _, format := range selected {
		if !isHLS(format) {
			continue
		}
		playlist, err := loadHLS(ctx, g, format, "best", 0)
		if err != nil {
			return 0, err
		}
		if math.Abs(float64(playlist.DurationMS)-info.Duration*1000) > 1000 {
			return 0, &sourceProblem{"platform_unavailable", "The recording is incomplete or its timeline is unavailable. Use a completed recording"}
		}
		durationMS = playlist.DurationMS
	}
	return durationMS, nil
}

func inspectCachedPlatformSource(ctx context.Context, g *networkGuard, info platformInfo, cached bool, refresh func() (platformInfo, error)) (platformInfo, int64, error) {
	durationMS, err := inspectPlatformSource(ctx, g, info)
	if err != nil && cached && cachedAddressDenied(err) && ctx.Err() == nil {
		info, err = refresh()
		if err == nil {
			durationMS, err = inspectPlatformSource(ctx, g, info)
		}
	}
	return info, durationMS, err
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, owner string) {
	s.uploadWithPublisher(w, r, owner, s.databaseAddSource)
}

func (s *Server) uploadWithPublisher(w http.ResponseWriter, r *http.Request, owner string, publish func(context.Context, Source) error) {
	ctx, cancel := context.WithTimeout(r.Context(), uploadTimeout(s.Config))
	defer cancel()
	r = r.WithContext(ctx)
	// Request contexts do not interrupt a server-side Body.Read. Set a socket
	// deadline as well so a stalled multipart body cannot occupy a slot forever.
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetWriteDeadline(deadline.Add(time.Minute))
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		internalError(w, err)
		return
	}
	// Handler ends any unread-body drain; clearing this deadline here would
	// allow a rejected upload to keep a server connection occupied afterward.
	reserve := s.Config.MaxSourceBytes
	if r.ContentLength > 0 {
		reserve = min(reserve, r.ContentLength)
	}
	if r.ContentLength > s.Config.MaxSourceBytes+(2<<20) {
		writeError(w, 413, "source_too_large", "The upload exceeds the file size limit")
		return
	}
	if err := s.beginSourceWithBytes(ctx, owner, reserve); err != nil {
		writeSourceAdmissionError(w, ctx, err)
		return
	}
	defer s.endSource(owner)
	r.Body = http.MaxBytesReader(w, r.Body, s.Config.MaxSourceBytes+(2<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, 400, "invalid_upload", "Send a multipart upload with a file field")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		if sourceTimedOut(ctx, err) {
			writeError(w, 504, "source_timeout", "Upload timed out; try again with a smaller file")
		} else {
			writeError(w, 400, "invalid_upload", "Choose a file to upload")
		}
		return
	}
	v := Source{ID: newID("src"), Owner: owner, StorageToken: s.sourceToken(owner), Kind: "upload", Title: filepath.Base(part.FileName())}
	s.sourceIdentity(owner, v.ID)
	if len(v.Title) > 200 {
		v.Title = "Uploaded video"
	}
	file, path, err := makeSourceFile(s.Config, v.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	keep := false
	defer func() {
		file.Close()
		if !keep {
			_ = removeSourcePreparation(path, v.Path)
		}
	}()
	if err = copyBounded(file, part, s.Config.MaxSourceBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if sourceTimedOut(ctx, err) {
			writeError(w, 504, "source_timeout", "Upload timed out; try again with a smaller file")
		} else if errors.Is(err, errSourceTooLarge) || errors.As(err, &tooLarge) {
			writeError(w, 413, "source_too_large", "The upload exceeds the file size limit")
		} else {
			writeError(w, 400, "invalid_upload", "The upload was interrupted; try uploading the file again")
		}
		return
	}
	if err = file.Close(); err != nil {
		internalError(w, err)
		return
	}
	if extra, e := reader.NextPart(); e != io.EOF {
		if extra != nil {
			extra.Close()
		}
		if sourceTimedOut(ctx, e) {
			writeError(w, 504, "source_timeout", "Upload timed out; try again with a smaller file")
		} else {
			writeError(w, 400, "invalid_upload", "Upload one file at a time")
		}
		return
	}
	v.Path = path
	if err = s.completeLocalSource(ctx, &v); err != nil {
		if sourceTimedOut(ctx, err) {
			writeError(w, 504, "source_timeout", "Source inspection timed out; try again with a smaller file")
		} else {
			writeError(w, 422, "unsupported_media", "This file has no supported finite video or audio stream")
		}
		return
	}
	if err = publishSourceMedia(ctx, s.Config, &v); err != nil {
		writeSourcePersistenceError(w, ctx, err)
		return
	}
	if err = publish(ctx, v); err != nil {
		keep = errors.Is(err, ErrSourceCommitUncertain)
		if keep {
			s.holdSourceReservation(owner)
		}
		writeSourcePersistenceError(w, ctx, err)
		return
	}
	keep = true
	preview := "/api/v1/sources/" + v.ID + "/media"
	v.PreviewURL = &preview
	completeSourcePresentation(&v)
	if persisted, err := s.databaseSource(ctx, v.ID, owner); err == nil {
		v = persisted
	}
	writeJSON(w, 201, v)
}

func (s *Server) source(w http.ResponseWriter, r *http.Request, owner string) {
	v, err := s.databaseSource(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) sourceMedia(w http.ResponseWriter, r *http.Request, owner string) {
	v, err := s.databaseSource(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if v.Path == "" {
		writeError(w, 404, "preview_unavailable", "A native preview is not available for this source")
		return
	}
	serveFile(w, r, v.Path, v.Title, false)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request, owner string) {
	var request ExportRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	v, err := s.databaseSource(r.Context(), request.SourceID, owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	if err = request.Validate(s.Config, v); err != nil {
		writeError(w, 400, "invalid_export", err.Error())
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		writeError(w, 400, "invalid_export", "Idempotency key is too long")
		return
	}
	if !s.allow("export:"+owner, 10) {
		writeError(w, 429, "rate_limit", "Too many exports; wait a minute")
		return
	}
	j, err := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) {
		return s.Store.CreateJobLimited(ctx, owner, request, key, 3, s.Config.MaxActiveJobs)
	})
	if errors.Is(err, ErrJobTooLarge) {
		writeError(w, 413, "storage_limit", "This export exceeds the server storage budget; select fewer fragments")
		return
	}
	if errors.Is(err, ErrBusy) {
		writeError(w, 429, "job_limit", "Wait for an active export before creating another")
		return
	}
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 202, j)
}

func (s *Server) job(w http.ResponseWriter, r *http.Request, owner string) {
	j, err := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) {
		return s.Store.Job(ctx, r.PathValue("id"), owner)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, j)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, owner string) {
	if err := apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		return s.Store.Cancel(ctx, r.PathValue("id"), owner)
	}); err != nil {
		lookupError(w, err)
		return
	}
	s.job(w, r, owner)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request, owner string) {
	type location struct{ path, name string }
	result, err := apiDatabase(r.Context(), func(ctx context.Context) (location, error) {
		path, name, err := s.Store.ArtifactPath(ctx, r.PathValue("id"), owner)
		return location{path, name}, err
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	// Large results can outlive the server's ordinary ten-minute response limit.
	// ServeContent retains HTTP range/If-Range support for interrupted downloads.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(12 * time.Hour))
	serveFile(w, r, result.path, result.name, true)
}

func serveFile(w http.ResponseWriter, r *http.Request, path, name string, attachment bool) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, 404, "expired", "The file has expired or is no longer available")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		internalError(w, err)
		return
	}
	if attachment {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	buffer := make([]byte, 512)
	n, _ := f.Read(buffer)
	_, _ = f.Seek(0, io.SeekStart)
	contentType := http.DetectContentType(buffer[:n])
	if !strings.HasPrefix(contentType, "video/") && !strings.HasPrefix(contentType, "audio/") && contentType != "application/ogg" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if !attachment {
		privateSourceFileHeaders(w, name, info)
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	// JSON metadata is small. Do not give stalled JSON bodies the server's
	// much longer media-upload timeout, which retains a connection/goroutine.
	deadline := time.Now().Add(jsonReadTimeout)
	if parent, ok := r.Context().Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		internalError(w, err)
		return err
	}
	complete := false
	defer func() {
		if complete {
			_ = controller.SetReadDeadline(time.Time{})
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		if r.ProtoMajor == 1 {
			w.Header().Set("Connection", "close")
		}
		if sourceTimedOut(r.Context(), err) {
			writeError(w, http.StatusRequestTimeout, "request_timeout", "The request timed out; try again")
		} else {
			writeError(w, 400, "invalid_request", "Send a valid JSON request with supported fields")
		}
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		if r.ProtoMajor == 1 {
			w.Header().Set("Connection", "close")
		}
		if sourceTimedOut(r.Context(), err) {
			writeError(w, http.StatusRequestTimeout, "request_timeout", "The request timed out; try again")
		} else {
			writeError(w, 400, "invalid_request", "Send exactly one JSON object")
		}
		return errors.New("trailing JSON")
	}
	complete = true
	return nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]APIError{"error": {Code: code, Message: message}})
}
func lookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "not_found", "The resource was not found or is not accessible in this session")
		return
	}
	internalError(w, err)
}
func internalError(w http.ResponseWriter, err error) {
	if errors.Is(err, errDatabaseUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is temporarily unavailable; try again")
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusRequestTimeout, "request_timeout", "The request timed out; try again")
		return
	}
	if errors.Is(err, ErrStorageInitializing) {
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "storage_initializing", "Storage accounting is being prepared; try again shortly")
		return
	}
	slog.Error("request failed", "error", fmt.Sprintf("%T", err))
	writeError(w, 500, "internal", "The operation could not be completed; try again")
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request, owner string) {
	paths, err := apiDatabase(r.Context(), func(ctx context.Context) ([]string, error) {
		return s.Store.DeleteSource(ctx, r.PathValue("id"), owner)
	})
	if errors.Is(err, ErrSourceInUse) {
		writeError(w, 409, "source_in_use", "Cancel or finish active exports before deleting this source")
		return
	}
	if err != nil {
		lookupError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), workerDatabaseTimeout)
	defer cancel()
	if err = s.Store.DrainStorageDeletes(ctx, s.Config, paths); err != nil {
		slog.Warn("source files awaiting deletion", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sourceToken(owner string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preparationTokens[owner]
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request, owner string) {
	sources, err := apiDatabase(r.Context(), func(ctx context.Context) ([]Source, error) {
		return s.Store.Sources(ctx, owner)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sources": sources})
}

func (s *Server) holdSourceReservation(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uncertainPreparations[owner] = true
}

func (s *Server) sourceIdentity(owner, id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		s.preparationSources[owner] = id
	}
	return s.preparationSources[owner]
}
