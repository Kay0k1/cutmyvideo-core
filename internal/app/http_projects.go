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
	"math"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type AgentProjectJob struct {
	ClipID  string        `json:"clip_id"`
	Job     Job           `json:"job"`
	Request ExportRequest `json:"request"`
	Current bool          `json:"current"`
}
type AgentProjectClip struct {
	ClipID  string        `json:"clip_id"`
	Request ExportRequest `json:"request"`
}
type AgentProject struct {
	Version         int                `json:"version"`
	ID              string             `json:"id"`
	Revision        int64              `json:"revision"`
	SourceID        string             `json:"source_id"`
	Source          Source             `json:"source"`
	Plan            CollectionPlan     `json:"plan"`
	ClipIDs         []string           `json:"clip_ids,omitempty"`
	Rendition       string             `json:"rendition"`
	Jobs            []AgentProjectJob  `json:"jobs"`
	Clips           []AgentProjectClip `json:"clips"`
	ExpiresAt       time.Time          `json:"expires_at"`
	AccessExpiresAt *time.Time         `json:"access_expires_at,omitempty"`
	Owner           string             `json:"-"`
	CollectionKey   string             `json:"-"`
}
type projectCreateRequest struct {
	SourceID      string           `json:"source_id"`
	Plan          CollectionPlan   `json:"plan"`
	ClipIDs       []string         `json:"clip_ids,omitempty"`
	Rendition     string           `json:"rendition,omitempty"`
	CollectionKey string           `json:"collection_key"`
	JobIDs        []string         `json:"job_ids,omitempty"`
	ClipJobs      []projectClipJob `json:"clip_jobs,omitempty"`
}
type projectClipJob struct {
	ClipID string `json:"clip_id"`
	JobID  string `json:"job_id"`
}
type projectEditRequest struct {
	Revision  int64          `json:"revision"`
	Plan      CollectionPlan `json:"plan"`
	ClipIDs   []string       `json:"clip_ids,omitempty"`
	Rendition string         `json:"rendition,omitempty"`
}
type projectScopeKey struct{}
type projectScope struct {
	ID, Actor string
	Current   map[string]Job
}

var errProjectConflict = errors.New("project revision conflict")
var errProjectLimit = errors.New("project limit reached")
var errProjectSelection = errors.New("invalid project selection")

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

const projectAccess = `(owner=$2 OR EXISTS(SELECT 1 FROM agent_project_grants g WHERE g.project_id=agent_projects.id AND g.recipient=$2 AND g.expires_at>clock_timestamp()))`

func projectPrepared(plan CollectionPlan, selected []string, rendition string, source Source, c Config, identityOnly bool) ([]PreparedCollectionClip, error) {
	if plan.Input != "" {
		return nil, collectionInvalid("", "plan.input", "HTTP projects cannot contain filesystem inputs")
	}
	if identityOnly {
		c.MaxRanges = 128 * 128
		c.MaxRangeMS = math.MaxInt64
		c.MaxJobMS = math.MaxInt64
		source.DurationMS = math.MaxInt64
		source.Path = "project-identity-only"
	}
	return NormalizeCollection(plan, CollectionExecution{ClipIDs: selected, Rendition: rendition}, c, source)
}

func (s *Store) agentProject(ctx context.Context, id, actor string, jobs bool) (AgentProject, error) {
	p := AgentProject{Version: 1, Jobs: []AgentProjectJob{}}
	var plan, selection, snapshot []byte
	err := s.DB.QueryRow(ctx, `SELECT id,owner,source_id,plan,clip_ids,rendition,collection_key,revision,expires_at,source_snapshot,(SELECT expires_at FROM agent_project_grants WHERE project_id=agent_projects.id AND recipient=$2 AND expires_at>clock_timestamp()) FROM agent_projects WHERE id=$1 AND expires_at>clock_timestamp() AND `+projectAccess, id, actor).Scan(&p.ID, &p.Owner, &p.SourceID, &plan, &selection, &p.Rendition, &p.CollectionKey, &p.Revision, &p.ExpiresAt, &snapshot, &p.AccessExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(plan, &p.Plan); err != nil {
		return p, err
	}
	if err = json.Unmarshal(selection, &p.ClipIDs); err != nil {
		return p, err
	}
	if err = json.Unmarshal(snapshot, &p.Source); err != nil {
		return p, err
	}
	if source, e := s.Source(ctx, p.SourceID, p.Owner); e == nil {
		p.Source = source
	} else if errors.Is(e, ErrNotFound) {
		p.Source.MediaRemoved = true
		p.Source.PreviewURL = nil
		p.Source.ThumbnailURL = nil
	} else {
		return p, e
	}
	if p.Owner == actor {
		p.AccessExpiresAt = nil
	}
	prepared, e := projectPrepared(p.Plan, p.ClipIDs, p.Rendition, p.Source, s.storageConfig(), true)
	if e != nil {
		return p, e
	}
	p.Clips = []AgentProjectClip{}
	for _, clip := range prepared {
		p.Clips = append(p.Clips, AgentProjectClip{ClipID: clip.ID, Request: clip.Request})
	}
	if jobs {
		if err = s.projectJobs(ctx, &p); err != nil {
			return p, err
		}
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

func (s *Store) projectJobs(ctx context.Context, p *AgentProject) error {
	clips, err := projectPrepared(p.Plan, nil, p.Rendition, p.Source, s.storageConfig(), true)
	if err != nil {
		return err
	}
	expected := map[string]ExportRequest{}
	for _, clip := range p.Clips {
		expected[clip.ClipID] = clip.Request
	}
	rows, err := s.DB.Query(ctx, `SELECT j.id,p.clip_id,j.request FROM agent_project_jobs p JOIN jobs j ON j.id=p.job_id WHERE p.project_id=$1 AND j.owner=$2 ORDER BY j.created_at DESC,j.id DESC LIMIT 1024`, p.ID, p.Owner)
	if err != nil {
		return err
	}
	type candidate struct {
		id, clip string
		request  ExportRequest
		current  bool
	}
	chosen := map[string]candidate{}
	for rows.Next() {
		var c candidate
		var raw []byte
		if err = rows.Scan(&c.id, &c.clip, &raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &c.request); err != nil {
			break
		}
		want, exists := expected[c.clip]
		c.current = exists && reflect.DeepEqual(c.request, want)
		old, found := chosen[c.clip]
		if !found || (!old.current && c.current) {
			chosen[c.clip] = c
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, clip := range clips {
		if c, ok := chosen[clip.ID]; ok {
			job, e := s.Job(ctx, c.id, p.Owner)
			if e != nil {
				return e
			}
			rewriteProjectJobID(p.ID, &job)
			p.Jobs = append(p.Jobs, AgentProjectJob{ClipID: c.clip, Job: job, Request: c.request, Current: c.current})
		}
	}
	return nil
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
	retained, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) { return s.Store.agentProject(ctx, id, owner, true) })
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
	created := true
	err = apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		tx, e := s.Store.DB.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(context.Background())
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "agent-project:"+owner); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM agent_projects WHERE id IN (SELECT id FROM agent_projects WHERE expires_at<clock_timestamp() ORDER BY expires_at,id LIMIT 200)`); e != nil {
			return e
		}
		var savedSource string
		e = tx.QueryRow(ctx, `SELECT source_id FROM agent_projects WHERE owner=$1 AND collection_key=$2`, owner, body.CollectionKey).Scan(&savedSource)
		if e == nil {
			if savedSource != body.SourceID {
				return errProjectConflict
			}
			created = false
			return tx.Commit(ctx)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_projects WHERE owner=$1`, owner).Scan(&n); e != nil {
			return e
		}
		if n >= 20 {
			return errProjectLimit
		}
		if _, e = tx.Exec(ctx, `INSERT INTO agent_projects(id,owner,source_id,plan,source_snapshot,clip_ids,rendition,collection_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, owner, source.ID, plan, snapshot, selection, collectionRendition(body.Rendition), body.CollectionKey); e != nil {
			return e
		}
		for job, clip := range attached {
			if _, e = tx.Exec(ctx, `INSERT INTO agent_project_jobs(project_id,job_id,clip_id) VALUES($1,$2,$3)`, id, job, clip); e != nil {
				return e
			}
		}
		return tx.Commit(ctx)
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
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) { return s.Store.agentProject(ctx, id, actor, true) })
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
		return s.Store.agentProject(ctx, r.PathValue("project"), actor, false)
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
		tag, e := s.Store.DB.Exec(ctx, `UPDATE agent_projects SET plan=$4,clip_ids=$5,rendition=$6,revision=revision+1 WHERE id=$1 AND `+projectAccess+` AND expires_at>clock_timestamp() AND revision=$3`, p.ID, actor, body.Revision, plan, selection, collectionRendition(body.Rendition))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return errProjectConflict
		}
		return nil
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
	var expiry time.Time
	err := apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		tx, e := s.Store.DB.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(context.Background())
		var owner string
		if e = tx.QueryRow(ctx, `SELECT owner FROM agent_projects WHERE id=$1 AND owner=$2 AND expires_at>clock_timestamp() FOR UPDATE`, r.PathValue("project"), actor).Scan(&owner); errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM agent_project_handoffs WHERE project_id=$1 AND expires_at<clock_timestamp()`, r.PathValue("project")); e != nil {
			return e
		}
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_handoffs WHERE project_id=$1`, r.PathValue("project")).Scan(&n); e != nil {
			return e
		}
		if n >= 8 {
			return errProjectLimit
		}
		if e = tx.QueryRow(ctx, `INSERT INTO agent_project_handoffs(token_hash,project_id) VALUES($1,$2) RETURNING expires_at`, hash, r.PathValue("project")).Scan(&expiry); e != nil {
			return e
		}
		return tx.Commit(ctx)
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
	var projectID string
	err := apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		tx, e := s.Store.DB.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(context.Background())
		var recipient *string
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-recipient:"+actor); e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT h.project_id,h.recipient FROM agent_project_handoffs h JOIN agent_projects p ON p.id=h.project_id WHERE h.token_hash=$1 AND h.expires_at>clock_timestamp() AND p.expires_at>clock_timestamp() FOR UPDATE OF h,p`, hash).Scan(&projectID, &recipient); errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		}
		if recipient != nil {
			if *recipient != actor {
				return ErrNotFound
			}
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_project_grants WHERE project_id=$1 AND recipient=$2 AND expires_at>clock_timestamp())`, projectID, actor).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return ErrNotFound
			}
			return tx.Commit(ctx)
		}
		if _, e = tx.Exec(ctx, `DELETE FROM agent_project_grants WHERE expires_at<clock_timestamp() AND (project_id=$1 OR recipient=$2)`, projectID, actor); e != nil {
			return e
		}
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_grants WHERE recipient=$1`, actor).Scan(&n); e != nil {
			return e
		}
		if n >= 20 {
			return errProjectLimit
		}
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_grants WHERE project_id=$1`, projectID).Scan(&n); e != nil {
			return e
		}
		if n >= 32 {
			return errProjectLimit
		}
		if _, e = tx.Exec(ctx, `INSERT INTO agent_project_grants(project_id,recipient,expires_at) VALUES($1,$2,least(clock_timestamp()+interval '24 hours',(SELECT expires_at FROM agent_projects WHERE id=$1))) ON CONFLICT(project_id,recipient) DO NOTHING`, projectID, actor); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE agent_project_handoffs SET recipient=$2,redeemed_at=clock_timestamp() WHERE token_hash=$1`, hash, actor); e != nil {
			return e
		}
		return tx.Commit(ctx)
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
		return s.Store.agentProject(ctx, projectID, actor, true)
	})
	if err != nil {
		lookupError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project": p})
}

func (s *Server) revokeAgentProject(w http.ResponseWriter, r *http.Request, actor string, remove bool) {
	err := apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		tx, e := s.Store.DB.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(context.Background())
		var id string
		if e = tx.QueryRow(ctx, `SELECT id FROM agent_projects WHERE id=$1 AND owner=$2 FOR UPDATE`, r.PathValue("project"), actor).Scan(&id); errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		}
		if remove {
			_, e = tx.Exec(ctx, `DELETE FROM agent_projects WHERE id=$1`, id)
		} else {
			_, e = tx.Exec(ctx, `DELETE FROM agent_project_handoffs WHERE project_id=$1`, id)
			if e == nil {
				_, e = tx.Exec(ctx, `DELETE FROM agent_project_grants WHERE project_id=$1`, id)
			}
		}
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
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
		return s.Store.agentProject(ctx, r.PathValue("project"), actor, true)
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
		return s.Store.agentProject(ctx, r.PathValue("project"), actor, true)
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
	err = apiDatabaseExec(r.Context(), func(ctx context.Context) error {
		tx, e := s.Store.DB.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(context.Background())
		var revision int64
		if e = tx.QueryRow(ctx, `SELECT revision FROM agent_projects WHERE id=$1 AND expires_at>clock_timestamp() AND `+projectAccess+` FOR UPDATE`, p.ID, actor).Scan(&revision); errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		}
		var stored string
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT intent_hash,request,attempt_key FROM agent_project_retries WHERE project_id=$1 AND key=$2`, p.ID, key).Scan(&stored, &raw, &attempt)
		if e == nil {
			if stored != hash {
				return errProjectConflict
			}
			if e = json.Unmarshal(raw, &request); e != nil {
				return e
			}
			return tx.Commit(ctx)
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if revision != body.Revision {
			return errProjectConflict
		}
		allowed, seen := map[string]bool{}, map[string]bool{}
		for _, clip := range p.Clips {
			allowed[clip.ClipID] = true
		}
		for _, id := range body.ClipIDs {
			if !allowed[id] || seen[id] {
				return errProjectSelection
			}
			seen[id] = true
		}
		clips, e := projectPrepared(p.Plan, body.ClipIDs, p.Rendition, p.Source, s.Config, true)
		if e != nil {
			return e
		}
		current := map[string]AgentProjectJob{}
		for _, row := range p.Jobs {
			if row.Current {
				current[row.ClipID] = row
			}
		}
		for _, clip := range clips {
			row, ok := current[clip.ID]
			if !ok {
				return errProjectConflict
			}
			job, jobErr := readJob(ctx, tx, row.Job.ID, p.Owner)
			if jobErr != nil {
				return jobErr
			}
			if !projectRetryableJob(job, time.Now()) {
				return errProjectConflict
			}
			// The previous intent is durable before its job is admitted. A second
			// caller key must not race that gap and create two repair renders.
			var previousKey string
			e = tx.QueryRow(ctx, `SELECT clip_job_keys->>$2 FROM agent_project_retries WHERE project_id=$1 AND clip_intents->>$2=$3 ORDER BY created_at DESC,key DESC LIMIT 1`, p.ID, clip.ID, collectionJobKey("", clip)).Scan(&previousKey)
			if e == nil {
				var previousID string
				if e = tx.QueryRow(ctx, `SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2`, p.Owner, previousKey).Scan(&previousID); errors.Is(e, pgx.ErrNoRows) {
					return errProjectConflict
				} else if e != nil {
					return e
				}
				previous, previousErr := readJob(ctx, tx, previousID, p.Owner)
				if previousErr != nil {
					return previousErr
				}
				if !projectRetryableJob(previous, time.Now()) {
					return errProjectConflict
				}
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
		}
		var count int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_retries WHERE project_id=$1`, p.ID).Scan(&count); e != nil {
			return e
		}
		if count >= 1024 {
			return errProjectLimit
		}
		raw, e = json.Marshal(request)
		if e != nil {
			return e
		}
		clipIntents, clipJobKeys := map[string]string{}, map[string]string{}
		for _, clip := range clips {
			clipIntents[clip.ID] = collectionJobKey("", clip)
			clipJobKeys[clip.ID] = collectionJobKey(attempt, clip)
		}
		intentsJSON, _ := json.Marshal(clipIntents)
		keysJSON, _ := json.Marshal(clipJobKeys)
		if _, e = tx.Exec(ctx, `INSERT INTO agent_project_retries(project_id,key,intent_hash,request,attempt_key,clip_intents,clip_job_keys) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, key, hash, raw, attempt, intentsJSON, keysJSON); e != nil {
			return e
		}
		return tx.Commit(ctx)
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
	data, _ := json.Marshal(request)
	inner := r.Clone(context.WithValue(r.Context(), projectScopeKey{}, projectScope{ID: p.ID, Actor: actor}))
	inner.Body = ioReadCloser{bytes.NewReader(data)}
	inner.ContentLength = int64(len(data))
	inner.Header = r.Header.Clone()
	inner.Header.Set("Idempotency-Key", attempt)
	s.submitCollection(w, inner, p.Owner)
}

func projectRetryableJob(job Job, now time.Time) bool {
	if job.Status == "failed" || job.Status == "cancelled" {
		return true
	}
	if job.Status != "succeeded" || len(job.Items) == 0 {
		return false
	}
	for _, item := range job.Items {
		if item.Artifact != nil && (item.Artifact.ExpiresAt == nil || item.Artifact.ExpiresAt.After(now)) {
			return false
		}
	}
	return true
}

type ioReadCloser struct{ *bytes.Reader }

func (ioReadCloser) Close() error { return nil }

func (s *Server) recordScopedProjectJob(ctx context.Context, owner, clip string, job Job) error {
	scope, ok := ctx.Value(projectScopeKey{}).(projectScope)
	if !ok {
		return nil
	}
	return apiDatabaseExec(ctx, func(dbCtx context.Context) error {
		tag, err := s.Store.DB.Exec(dbCtx, `INSERT INTO agent_project_jobs(project_id,job_id,clip_id) SELECT id,$4,$5 FROM agent_projects WHERE id=$1 AND owner=$3 AND source_id=$6 AND expires_at>clock_timestamp() AND `+projectAccess+` AND (SELECT count(*) FROM agent_project_jobs WHERE project_id=$1)<1024 ON CONFLICT(project_id,job_id) DO NOTHING`, scope.ID, scope.Actor, owner, job.ID, clip, job.Request.SourceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			err = s.Store.DB.QueryRow(dbCtx, `SELECT EXISTS(SELECT 1 FROM agent_project_jobs WHERE project_id=$1 AND job_id=$2)`, scope.ID, job.ID).Scan(&exists)
			if err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
		return nil
	})
}

func (s *Server) scopedProjectResource(w http.ResponseWriter, r *http.Request, actor string) {
	p, err := apiDatabase(r.Context(), func(ctx context.Context) (AgentProject, error) {
		return s.Store.agentProject(ctx, r.PathValue("project"), actor, false)
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
			var yes bool
			query := `SELECT EXISTS(SELECT 1 FROM agent_project_jobs WHERE project_id=$1 AND job_id=$2)`
			if parts[0] == "artifacts" {
				query = `SELECT EXISTS(SELECT 1 FROM agent_project_jobs p JOIN artifacts a ON a.job_id=p.job_id WHERE p.project_id=$1 AND a.id=$2 AND a.owner=$3)`
			}
			var err error
			if parts[0] == "artifacts" {
				err = s.Store.DB.QueryRow(ctx, query, p.ID, parts[1], p.Owner).Scan(&yes)
			} else {
				err = s.Store.DB.QueryRow(ctx, query, p.ID, parts[1]).Scan(&yes)
			}
			return yes, err
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
