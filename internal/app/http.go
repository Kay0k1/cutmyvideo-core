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
	Config    Config
	Store     *Store
	mu        sync.Mutex
	preparing map[string]bool
	rate      map[string]*rateEntry
	slots     chan struct{}
}
type rateEntry struct {
	count int
	reset time.Time
}

var (
	errSourceLimit      = &sourceProblem{"source_limit", "This session has reached its source limit; wait for older sources to expire"}
	errSourceBusy       = &sourceProblem{"source_busy", "Wait for the current source preparation or cancel it before opening another"}
	errSourceStorage    = &sourceProblem{"storage_limit", "Source storage is full; use a smaller file or wait for older files to expire"}
	errSourceServerBusy = &sourceProblem{"server_busy", "The server is preparing other sources; try again shortly"}
)

func NewServer(c Config, s *Store) *Server {
	return &Server{Config: c, Store: s, preparing: map[string]bool{}, rate: map[string]*rateEntry{}, slots: make(chan struct{}, 4)}
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
	mux.HandleFunc("POST /api/v1/sources", s.withSession(s.addSource))
	mux.HandleFunc("POST /api/v1/uploads", s.withSession(s.upload))
	mux.HandleFunc("GET /api/v1/sources/{id}", s.withSession(s.source))
	mux.HandleFunc("GET /api/v1/sources/{id}/media", s.withSession(s.sourceMedia))
	mux.HandleFunc("GET /api/v1/sources/{id}/thumbnail", s.withSession(s.sourceThumbnail))
	mux.HandleFunc("POST /api/v1/jobs", s.withSession(s.createJob))
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.withSession(s.job))
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.withSession(s.cancelJob))
	mux.HandleFunc("GET /api/v1/artifacts/{id}/download", s.withSession(s.download))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "POST" && !s.validOrigin(r) {
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
	if len(s.rate) > 10000 {
		for k, v := range s.rate {
			if v.reset.Before(now) {
				delete(s.rate, k)
			}
		}
		if len(s.rate) > 10000 {
			return false
		}
	}
	v := s.rate[key]
	if v == nil || v.reset.Before(now) {
		v = &rateEntry{reset: now.Add(time.Minute)}
		s.rate[key] = v
	}
	v.count++
	return v.count <= limit
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
	writeJSON(w, 200, map[string]bool{"ok": true})
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
		if !s.allow("requests:"+owner, 180) {
			writeError(w, 429, "rate_limit", "Too many requests; wait a minute")
			return
		}
		if r.Method == "POST" && !s.allow("mutations:"+s.clientIP(r), s.Config.MutationsPerMinute) {
			writeError(w, 429, "rate_limit", "Too many changes; wait a minute")
			return
		}
		next(w, r, owner)
	}
}

func (s *Server) beginSource(ctx context.Context, owner string) error {
	// Reserve the owner's preparation slot before reading quota state. A second
	// request must not carry an old count/size past the first request's completion.
	s.mu.Lock()
	if s.preparing[owner] {
		s.mu.Unlock()
		return errSourceBusy
	}
	if !storageAvailableContext(ctx, s.Config, int64(len(s.preparing)+1)*s.Config.MaxSourceBytes) {
		s.mu.Unlock()
		return errSourceStorage
	}
	select {
	case s.slots <- struct{}{}:
		s.preparing[owner] = true
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
	var count int
	err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM sources WHERE owner=$1`, owner).Scan(&count)
	if err != nil {
		return err
	}
	if count >= 20 {
		return errSourceLimit
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT path FROM sources WHERE owner=$1 AND path<>'' UNION ALL SELECT thumbnail_path FROM sources WHERE owner=$1 AND thumbnail_path<>''`, owner)
	if err != nil {
		return err
	}
	var ownedBytes int64
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if info, e := os.Stat(p); e == nil {
			ownedBytes += info.Size()
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if ownedBytes+s.Config.MaxSourceBytes > s.Config.MaxOwnerBytes {
		return errSourceStorage
	}
	accepted = true
	return nil
}

func sourceTimedOut(ctx context.Context, err error) bool {
	var networkError net.Error
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout()
}

func writeSourceAdmissionError(w http.ResponseWriter, ctx context.Context, err error) {
	if sourceTimedOut(ctx, err) {
		writeError(w, 504, "source_timeout", "Source preparation timed out; try again")
	} else if problem := problemFromError(err); problem != nil {
		writeError(w, 429, problem.code, problem.message)
	} else {
		internalError(w, err)
	}
}
func (s *Server) endSource(owner string) {
	s.mu.Lock()
	delete(s.preparing, owner)
	s.mu.Unlock()
	<-s.slots
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request, owner string) {
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
	ctx, cancel := context.WithTimeout(r.Context(), s.Config.SourceTimeout)
	defer cancel()
	if err = s.beginSource(ctx, owner); err != nil {
		writeSourceAdmissionError(w, ctx, err)
		return
	}
	defer s.endSource(owner)
	v := Source{ID: newID("src"), Owner: owner, URL: u.String(), Kind: "direct"}
	platform := isPlatformHost(u.Hostname())
	if !platform {
		req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		req.Header.Set("User-Agent", "cutmy-core/0.1")
		client := safeClient()
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
					_ = os.Remove(path)
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
			if e = s.Store.AddSource(ctx, v); e != nil {
				internalError(w, e)
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
		if e = s.Store.AddSource(ctx, v); e != nil {
			if v.ThumbnailPath != "" {
				_ = os.Remove(v.ThumbnailPath)
			}
			internalError(w, e)
			return
		}
		_ = s.Store.CachePlatformMetadata(ctx, v, info)
	}
	if v.Path != "" {
		preview := "/api/v1/sources/" + v.ID + "/media"
		v.PreviewURL = &preview
	}
	completeSourcePresentation(&v)
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
	ctx, cancel := context.WithTimeout(r.Context(), s.Config.SourceTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	// Request contexts do not interrupt a server-side Body.Read. Set a socket
	// deadline as well so a stalled multipart body cannot occupy a slot forever.
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		internalError(w, err)
		return
	}
	defer controller.SetReadDeadline(time.Time{})
	if err := s.beginSource(ctx, owner); err != nil {
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
	v := Source{ID: newID("src"), Owner: owner, Kind: "upload", Title: filepath.Base(part.FileName())}
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
			_ = os.Remove(path)
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
	if err = s.Store.AddSource(ctx, v); err != nil {
		internalError(w, err)
		return
	}
	keep = true
	preview := "/api/v1/sources/" + v.ID + "/media"
	v.PreviewURL = &preview
	completeSourcePresentation(&v)
	writeJSON(w, 201, v)
}

func (s *Server) source(w http.ResponseWriter, r *http.Request, owner string) {
	v, err := s.Store.Source(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) sourceMedia(w http.ResponseWriter, r *http.Request, owner string) {
	v, err := s.Store.Source(r.Context(), r.PathValue("id"), owner)
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
	v, err := s.Store.Source(r.Context(), request.SourceID, owner)
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
	j, err := s.Store.CreateJobLimited(r.Context(), owner, request, key, 3, s.Config.MaxActiveJobs)
	if errors.Is(err, ErrBusy) {
		writeError(w, 429, "job_limit", "Wait for an active export before creating another")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, 202, j)
}

func (s *Server) job(w http.ResponseWriter, r *http.Request, owner string) {
	j, err := s.Store.Job(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, j)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, owner string) {
	if err := s.Store.Cancel(r.Context(), r.PathValue("id"), owner); err != nil {
		lookupError(w, err)
		return
	}
	s.job(w, r, owner)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request, owner string) {
	path, name, err := s.Store.ArtifactPath(r.Context(), r.PathValue("id"), owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	serveFile(w, r, path, name, true)
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
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		writeError(w, 400, "invalid_request", "Send a valid JSON request with supported fields")
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid_request", "Send exactly one JSON object")
		return errors.New("trailing JSON")
	}
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
	slog.Error("request failed", "error", fmt.Sprintf("%T", err))
	writeError(w, 500, "internal", "The operation could not be completed; try again")
}
