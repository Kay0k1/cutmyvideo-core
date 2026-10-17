package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func writeProjectPersistenceError(w http.ResponseWriter, err error) {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && postgres.Code == "23514" &&
		(postgres.TableName == "agent_projects" && postgres.ConstraintName == "agent_projects_plan_check" ||
			postgres.TableName == "agent_project_retries" && postgres.ConstraintName == "agent_project_retries_request_check") {
		writeError(w, http.StatusBadRequest, "project_too_large", "Reduce the saved plan or split it into smaller projects")
		return
	}
	lookupError(w, err)
}

func (s *Server) agentProject(ctx context.Context, id, actor string, jobs bool) (AgentProject, error) {
	p, err := s.Store.agentProject(ctx, id, actor, jobs)
	if err != nil {
		return p, err
	}
	for i := range p.Jobs {
		rewriteProjectJobID(p.ID, &p.Jobs[i].Job)
	}
	projectSourceURLs(&p)
	return p, nil
}

func projectSourceURLs(p *AgentProject) {
	base := "/api/v1/projects/" + p.ID + "/sources/" + p.SourceID
	if p.Source.PreviewURL != nil && strings.HasPrefix(*p.Source.PreviewURL, "/api/v1/sources/") {
		v := base + "/media"
		p.Source.PreviewURL = &v
	}
	if p.Source.ThumbnailURL != nil && strings.HasPrefix(*p.Source.ThumbnailURL, "/api/v1/sources/") {
		v := base + "/thumbnail"
		p.Source.ThumbnailURL = &v
	}
}

func rewriteProjectJobID(projectID string, job *Job) {
	for i := range job.Items {
		if job.Items[i].Artifact != nil {
			artifact := *job.Items[i].Artifact
			artifact.DownloadURL = "/api/v1/projects/" + projectID + "/artifacts/" + artifact.ID + "/download"
			job.Items[i].Artifact = &artifact
		}
	}
}
func rewriteProjectJob(ctx context.Context, job *Job) {
	if scope, ok := ctx.Value(projectScopeKey{}).(projectScope); ok {
		rewriteProjectJobID(scope.ID, job)
	}
}

func scopedProjectExistingJob(ctx context.Context, clip PreparedCollectionClip) (Job, bool) {
	scope, ok := ctx.Value(projectScopeKey{}).(projectScope)
	if !ok {
		return Job{}, false
	}
	job, ok := scope.Current[clip.ID]
	return job, ok && reflect.DeepEqual(job.Request, clip.Request)
}

func (s *Server) createAgentProject(w http.ResponseWriter, r *http.Request, owner string) {
	var body projectCreateRequest
	if decodeJSON(w, r, &body) != nil {
		return
	}
	if strings.TrimSpace(body.CollectionKey) == "" || len(body.CollectionKey) > 128 || len(body.JobIDs)+len(body.ClipJobs) > 128 || len(body.JobIDs) > 0 && len(body.ClipJobs) > 0 {
		writeError(w, 400, "invalid_project", "Send a saved collection key and at most 128 job IDs or explicit clip/job pairs")
		return
	}
	identity, _ := json.Marshal([]string{owner, body.CollectionKey})
	digest := sha256.Sum256(identity)
	id := "project_" + hex.EncodeToString(digest[:])
	retained, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) { return s.agentProject(ctx, id, owner, true) })
	if err == nil {
		if retained.Owner != owner || retained.CollectionKey != body.CollectionKey || retained.SourceID != body.SourceID {
			writeError(w, 409, "project_conflict", "This project workflow key belongs to a different fixed source")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, 200, retained)
		return
	}
	if !errors.Is(err, ErrNotFound) {
		lookupError(w, err)
		return
	}
	if !s.allow("project-create:"+owner, 10) {
		writeError(w, 429, "rate_limit", "Too many projects; wait a minute")
		return
	}
	source, err := s.databaseSource(r.Context(), body.SourceID, owner)
	if err != nil {
		lookupError(w, err)
		return
	}
	clips, err := projectPrepared(body.Plan, body.ClipIDs, body.Rendition, source, s.Config, true)
	if err != nil {
		writeCollectionPlanError(w, err)
		return
	}
	attached := map[string]string{}
	if len(body.ClipJobs) > 0 {
		expected := make(map[string]ExportRequest, len(clips))
		for _, clip := range clips {
			expected[clip.ID] = clip.Request
		}
		seenClips := make(map[string]bool, len(body.ClipJobs))
		for _, pair := range body.ClipJobs {
			want, exists := expected[pair.ClipID]
			if !exists || seenClips[pair.ClipID] || attached[pair.JobID] != "" || pair.JobID == "" {
				writeError(w, 400, "invalid_project", "Clip/job pairs must name unique selected clips and unique jobs")
				return
			}
			job, e := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) { return s.Store.Job(ctx, pair.JobID, owner) })
			if e != nil {
				lookupError(w, e)
				return
			}
			if !reflect.DeepEqual(job.Request, want) {
				writeError(w, 400, "invalid_project", "Each attached job must match its selected clip's exact source and edit decisions")
				return
			}
			seenClips[pair.ClipID], attached[pair.JobID] = true, pair.ClipID
		}
	}
	for _, id := range body.JobIDs {
		if _, exists := attached[id]; exists {
			writeError(w, 400, "invalid_project", "Job IDs must be unique")
			return
		}
		job, e := apiDatabase(r.Context(), func(ctx context.Context) (Job, error) { return s.Store.Job(ctx, id, owner) })
		if e != nil {
			lookupError(w, e)
			return
		}
		if job.Request.SourceID != source.ID {
			writeError(w, 400, "invalid_project", "A project can include jobs only from its selected source")
			return
		}
		match := ""
		for _, clip := range clips {
			if reflect.DeepEqual(clip.Request, job.Request) {
				if match != "" {
					match = "ambiguous"
					break
				}
				match = clip.ID
			}
		}
		if match == "" || match == "ambiguous" {
			writeError(w, 400, "invalid_project", "Each attached job must match one unambiguous selected clip")
			return
		}
		attached[id] = match
	}
	plan, _ := json.Marshal(body.Plan)
	selection, _ := json.Marshal(body.ClipIDs)
	if body.ClipIDs == nil {
		selection = []byte("[]")
	}
	snapshot, _ := json.Marshal(source)
	created, err := apiDatabase(r.Context(), func(ctx context.Context) (bool, error) {
		return s.Store.createAgentProject(ctx, id, owner, body, source, attached, plan, selection, snapshot)
	})
	if errors.Is(err, errProjectConflict) {
		writeError(w, 409, "project_conflict", "This project workflow key belongs to a different fixed source")
		return
	}
	if errors.Is(err, errProjectLimit) {
		writeError(w, 429, "project_limit", "This owner has reached its retained project limit")
		return
	}
	if err != nil {
		writeProjectPersistenceError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	s.writeAgentProject(w, r, owner, id, status)
}

func (s *Server) writeAgentProject(w http.ResponseWriter, r *http.Request, actor, id string, status int) {
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) { return s.agentProject(ctx, id, actor, true) })
	if err != nil {
		lookupError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, p)
}

func (s *Server) getAgentProject(w http.ResponseWriter, r *http.Request, actor string) {
	s.writeAgentProject(w, r, actor, r.PathValue("project"), 200)
}

func (s *Server) editAgentProject(w http.ResponseWriter, r *http.Request, actor string) {
	var body projectEditRequest
	if decodeJSON(w, r, &body) != nil {
		return
	}
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) {
		return s.agentProject(ctx, r.PathValue("project"), actor, false)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	if _, err = projectPrepared(body.Plan, body.ClipIDs, body.Rendition, p.Source, s.Config, true); err != nil {
		writeCollectionPlanError(w, err)
		return
	}
	plan, _ := json.Marshal(body.Plan)
	selection, _ := json.Marshal(body.ClipIDs)
	if body.ClipIDs == nil {
		selection = []byte("[]")
	}
	err = apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		return s.Store.editAgentProject(ctx, p.ID, actor, body.Revision, plan, selection, body.Rendition)
	})
	if errors.Is(err, errProjectConflict) {
		writeError(w, 409, "project_conflict", "The project changed; reload it before saving edits")
		return
	}
	if err != nil {
		writeProjectPersistenceError(w, err)
		return
	}
	s.writeAgentProject(w, r, actor, p.ID, 200)
}

func (s *Server) issueProjectHandoff(w http.ResponseWriter, r *http.Request, actor string) {
	var empty struct{}
	if decodeJSONLimit(w, r, &empty, 1024) != nil {
		return
	}
	if !s.allow("project-handoff:"+actor, 10) {
		writeError(w, 429, "rate_limit", "Too many handoffs; wait a minute")
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		internalError(w, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	expiry, err := apiDatabase(r.Context(), func(ctx context.Context) (time.Time, error) {
		return s.Store.issueProjectHandoff(ctx, r.PathValue("project"), actor, hash)
	})
	if errors.Is(err, errProjectLimit) {
		writeError(w, 429, "handoff_limit", "This project has reached its unexpired handoff limit")
		return
	}
	if err != nil {
		lookupError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, 201, map[string]any{"project_id": r.PathValue("project"), "token": token, "expires_at": expiry})
}

func (s *Server) redeemProjectHandoff(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		Token string `json:"token"`
	}
	if decodeJSONLimit(w, r, &body, 1024) != nil {
		return
	}
	if !s.allow("project-redeem:"+actor, 20) {
		writeError(w, 429, "rate_limit", "Too many handoff attempts; wait a minute")
		return
	}
	decoded, e := base64.RawURLEncoding.DecodeString(body.Token)
	if e != nil || len(decoded) != 32 || len(body.Token) != 43 {
		writeError(w, 404, "not_found", "This project handoff is unavailable")
		return
	}
	sum := sha256.Sum256([]byte(body.Token))
	hash := hex.EncodeToString(sum[:])
	projectID, err := apiDatabase(r.Context(), func(ctx context.Context) (string, error) {
		return s.Store.redeemProjectHandoff(ctx, actor, hash)
	})
	if errors.Is(err, errProjectLimit) {
		writeError(w, 429, "project_limit", "The project access limit was reached")
		return
	}
	if err != nil {
		lookupError(w, err)
		return
	}
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) {
		return s.agentProject(ctx, projectID, actor, true)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project": p})
}

func (s *Server) revokeAgentProject(w http.ResponseWriter, r *http.Request, actor string, remove bool) {
	err := apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		return s.Store.revokeAgentProject(ctx, r.PathValue("project"), actor, remove)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) scopedProjectCollection(w http.ResponseWriter, r *http.Request, actor string, validate bool) {
	var selection struct {
		Revision *int64   `json:"revision,omitempty"`
		ClipIDs  []string `json:"clip_ids,omitempty"`
	}
	if decodeJSONLimit(w, r, &selection, 16<<10) != nil {
		return
	}
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) {
		return s.agentProject(ctx, r.PathValue("project"), actor, true)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	if selection.Revision != nil && *selection.Revision != p.Revision {
		writeError(w, http.StatusConflict, "project_conflict", "Reload the current project before submitting saved edits")
		return
	}
	selected := p.ClipIDs
	if selection.ClipIDs != nil {
		allowed := make(map[string]bool, len(p.Clips))
		for _, clip := range p.Clips {
			allowed[clip.ClipID] = true
		}
		seen := make(map[string]bool, len(selection.ClipIDs))
		if len(selection.ClipIDs) == 0 || len(selection.ClipIDs) > 128 {
			writeError(w, http.StatusBadRequest, "invalid_project_selection", "Select one or more clips from the saved project selection")
			return
		}
		for _, id := range selection.ClipIDs {
			if !allowed[id] || seen[id] {
				writeError(w, http.StatusBadRequest, "invalid_project_selection", "Select unique clips from the saved project selection")
				return
			}
			seen[id] = true
		}
		selected = selection.ClipIDs
	}
	body, _ := json.Marshal(CollectionRequest{SourceID: p.SourceID, Plan: p.Plan, ClipIDs: selected, Rendition: p.Rendition})
	current := map[string]Job{}
	for _, job := range p.Jobs {
		if job.Current {
			current[job.ClipID] = job.Job
		}
	}
	inner := r.Clone(context.WithValue(r.Context(), projectScopeKey{}, projectScope{ID: p.ID, Actor: actor, Current: current}))
	inner.Body = http.NoBody
	if len(body) > 0 {
		inner.Body = ioReadCloser{bytes.NewReader(body)}
	}
	inner.ContentLength = int64(len(body))
	inner.Header = r.Header.Clone()
	inner.Header.Set("Idempotency-Key", p.CollectionKey)
	if validate {
		s.validateCollection(w, inner, p.Owner)
	} else {
		s.submitCollection(w, inner, p.Owner)
	}
}

func (s *Server) retryProjectCollection(w http.ResponseWriter, r *http.Request, actor string) {
	var body struct {
		Revision int64    `json:"revision"`
		ClipIDs  []string `json:"clip_ids"`
	}
	if decodeJSONLimit(w, r, &body, 16<<10) != nil {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !validPreparationKey(key) || body.Revision < 1 || len(body.ClipIDs) < 1 || len(body.ClipIDs) > 128 {
		writeError(w, 400, "invalid_project_retry", "Send a saved retry key, project revision and selected failed clip IDs")
		return
	}
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) {
		return s.agentProject(ctx, r.PathValue("project"), actor, true)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	intent, _ := json.Marshal(body)
	sum := sha256.Sum256(intent)
	hash := hex.EncodeToString(sum[:])
	request := CollectionRequest{SourceID: p.SourceID, Plan: p.Plan, ClipIDs: body.ClipIDs, Rendition: p.Rendition}
	attempt := newID("project-attempt")
	retry, err := apiDatabase(r.Context(), func(ctx context.Context) (projectRetryRecord, error) {
		return s.Store.retryProjectCollection(ctx, p, actor, key, hash, attempt, body.Revision, body.ClipIDs, request, s.Config)
	})
	if errors.Is(err, errProjectConflict) {
		writeError(w, 409, "project_conflict", "Reconcile this retry or reload the current failed clips before starting another attempt")
		return
	}
	if errors.Is(err, errProjectSelection) {
		writeError(w, 400, "invalid_project_selection", "Select unique clips from the saved project selection")
		return
	}
	if errors.Is(err, errProjectLimit) {
		writeError(w, 429, "project_limit", "This project has reached its retained attempt limit")
		return
	}
	if err != nil {
		writeProjectPersistenceError(w, err)
		return
	}
	request, attempt = retry.Request, retry.Attempt
	data, _ := json.Marshal(request)
	inner := r.Clone(context.WithValue(r.Context(), projectScopeKey{}, projectScope{ID: p.ID, Actor: actor}))
	inner.Body = ioReadCloser{bytes.NewReader(data)}
	inner.ContentLength = int64(len(data))
	inner.Header = r.Header.Clone()
	inner.Header.Set("Idempotency-Key", attempt)
	s.submitCollection(w, inner, p.Owner)
}

type ioReadCloser struct{ *bytes.Reader }

func (ioReadCloser) Close() error { return nil }

func (s *Server) recordScopedProjectJob(ctx context.Context, owner, clip string, job Job) error {
	scope, ok := ctx.Value(projectScopeKey{}).(projectScope)
	if !ok {
		return nil
	}
	return apiDatabaseExec(ctx, func(dbCtx context.Context) error {
		return s.Store.bindProjectJob(dbCtx, projectJobBinding{Scope: scope, Clip: clip}, owner, job)
	})
}

func (s *Server) scopedProjectResource(w http.ResponseWriter, r *http.Request, actor string) {
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) {
		return s.agentProject(ctx, r.PathValue("project"), actor, false)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	parts := strings.Split(r.PathValue("rest"), "/")
	if len(parts) < 2 || len(parts) > 3 {
		lookupError(w, ErrNotFound)
		return
	}
	for key := range r.URL.Query() {
		if key == "source_id" || key == "owner" || key == "job_id" || key == "artifact_id" {
			writeError(w, 400, "invalid_project", "Project resource scope cannot be overridden")
			return
		}
	}
	inner := r.Clone(context.WithValue(r.Context(), projectScopeKey{}, projectScope{ID: p.ID, Actor: actor}))
	inner.SetPathValue("id", parts[1])
	var handler ownerHandler
	switch parts[0] {
	case "sources":
		if parts[1] != p.SourceID || r.Method != "GET" {
			lookupError(w, ErrNotFound)
			return
		}
		if len(parts) == 2 {
			writeJSON(w, 200, p.Source)
			return
		} else {
			switch parts[2] {
			case "media":
				handler = s.sourceMedia
			case "preview":
				handler = s.sourcePreview
			case "storyboard":
				handler = s.sourceStoryboard
			case "waveform":
				handler = s.sourceWaveform
			case "tracks":
				handler = s.sourceTracks
			case "thumbnail":
				handler = s.sourceThumbnail
			}
		}
	case "jobs", "artifacts":
		allowed, e := apiDatabase(r.Context(), func(ctx context.Context) (bool, error) {
			return s.Store.projectResourceAllowed(ctx, p.ID, parts[0], parts[1], p.Owner)
		})
		if e != nil {
			lookupError(w, e)
			return
		}
		if !allowed {
			lookupError(w, ErrNotFound)
			return
		}
		if parts[0] == "jobs" {
			if len(parts) == 2 && r.Method == "GET" {
				handler = s.job
			} else if len(parts) == 3 && parts[2] == "cancel" && r.Method == "POST" {
				handler = s.cancelJob
			} else if len(parts) == 3 && parts[2] == "download.zip" && r.Method == "GET" {
				handler = s.downloadArchive
			}
		} else if len(parts) == 3 && parts[2] == "download" && r.Method == "GET" {
			handler = s.download
		}
	}
	if handler == nil {
		lookupError(w, ErrNotFound)
		return
	}
	handler(w, inner, p.Owner)
}

func (s *Server) registerProjectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/projects", s.withSession(s.createAgentProject))
	mux.HandleFunc("POST /api/v1/projects/handoffs/redeem", s.withSession(s.redeemProjectHandoff))
	mux.HandleFunc("GET /api/v1/projects/{project}", s.withSession(s.getAgentProject))
	mux.HandleFunc("PUT /api/v1/projects/{project}", s.withSession(s.editAgentProject))
	mux.HandleFunc("POST /api/v1/projects/{project}/handoffs", s.withSession(s.issueProjectHandoff))
	mux.HandleFunc("DELETE /api/v1/projects/{project}/grants", s.withSession(func(w http.ResponseWriter, r *http.Request, o string) { s.revokeAgentProject(w, r, o, false) }))
	mux.HandleFunc("DELETE /api/v1/projects/{project}", s.withSession(func(w http.ResponseWriter, r *http.Request, o string) { s.revokeAgentProject(w, r, o, true) }))
	mux.HandleFunc("POST /api/v1/projects/{project}/collections/validate", s.withSession(func(w http.ResponseWriter, r *http.Request, o string) { s.scopedProjectCollection(w, r, o, true) }))
	mux.HandleFunc("POST /api/v1/projects/{project}/collections", s.withSession(func(w http.ResponseWriter, r *http.Request, o string) { s.scopedProjectCollection(w, r, o, false) }))
	mux.HandleFunc("POST /api/v1/projects/{project}/collections/retry", s.withSession(s.retryProjectCollection))
	mux.HandleFunc("/api/v1/projects/{project}/{rest...}", s.withSession(s.scopedProjectResource))
}
