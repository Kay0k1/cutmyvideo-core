package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

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
	preparation, wrapped, proceed := s.startSourcePreparation(w, r, owner, sourcePreparationIntent{Kind: "import", URL: u.String()})
	if !proceed {
		return
	}
	w = wrapped
	defer preparation.finish()
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
	v := Source{ID: preparationSource(preparation), Owner: owner, StorageToken: s.sourceToken(owner), URL: u.String(), Kind: "direct"}
	preparation.attach(&v)
	s.sourceIdentity(owner, sourceFileIdentity(v))
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
			file, path, e := makeSourceFile(s.Config, sourceFileIdentity(v))
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
			digest := sha256.New()
			if e = copyBounded(io.MultiWriter(file, digest), resp.Body, s.Config.MaxSourceBytes); e != nil {
				if sourceTimedOut(ctx, e) {
					writeError(w, 504, "source_timeout", "Source download timed out; try again or upload your file")
				} else if errors.Is(e, errSourceTooLarge) {
					writeError(w, 413, "source_too_large", "The source exceeds the download limit")
				} else {
					writeError(w, 422, "source_unavailable", "Source download was interrupted; try again or upload your file")
				}
				return
			}
			stat, statErr := file.Stat()
			if statErr != nil {
				internalError(w, statErr)
				return
			}
			v.Fingerprint = &SourceFingerprint{Kind: "content", SHA256: hex.EncodeToString(digest.Sum(nil)), SizeBytes: stat.Size()}
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
		v.Width, v.Height = platformSourceDimensions(info)
		// Image transfer is optional and may be slow. The authenticated endpoint
		// prepares it on demand after this metadata-only import has committed.
		if _, err := validateURL(info.Thumbnail); err == nil {
			thumbnail := "/api/v1/sources/" + v.ID + "/thumbnail"
			v.ThumbnailURL = &thumbnail
		}
		completeSourcePresentation(&v)
		identity := sha256.Sum256([]byte(v.URL))
		v.Fingerprint = &SourceFingerprint{Kind: "provider", Provider: v.Provider, ProviderID: v.ProviderID, CanonicalURLSHA256: hex.EncodeToString(identity[:])}
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
	inspected, err := InspectLocal(ctx, s.Config, v.Path)
	if err != nil {
		return err
	}
	v.DurationMS, v.Width, v.Height = inspected.DurationMS, inspected.Width, inspected.Height
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
	intent, valid := s.uploadPreparationIntent(w, r)
	if !valid {
		return
	}
	preparation, wrapped, proceed := s.startSourcePreparation(w, r, owner, intent)
	if !proceed {
		return
	}
	w = wrapped
	defer preparation.finish()
	if intent.SizeBytes > s.Config.MaxSourceBytes {
		writeError(w, 413, "source_too_large", "The upload exceeds the file size limit")
		return
	}
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
	v := Source{ID: preparationSource(preparation), Owner: owner, StorageToken: s.sourceToken(owner), Kind: "upload", Title: filepath.Base(part.FileName())}
	preparation.attach(&v)
	s.sourceIdentity(owner, sourceFileIdentity(v))
	if len(v.Title) > 200 {
		v.Title = "Uploaded video"
	}
	file, path, err := makeSourceFile(s.Config, sourceFileIdentity(v))
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
	digest := sha256.New()
	if err = copyBounded(io.MultiWriter(file, digest), part, s.Config.MaxSourceBytes); err != nil {
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
	stat, statErr := file.Stat()
	if statErr != nil {
		internalError(w, statErr)
		return
	}
	v.Fingerprint = &SourceFingerprint{Kind: "content", SHA256: hex.EncodeToString(digest.Sum(nil)), SizeBytes: stat.Size()}
	if err = verifyUploadedFingerprint(preparation, v.Fingerprint); err != nil {
		writeError(w, 422, "source_fingerprint_mismatch", "The received file does not match Upload-SHA256 and Upload-Size")
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
