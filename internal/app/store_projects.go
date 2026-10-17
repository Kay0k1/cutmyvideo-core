package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
)

const projectAccess = `(owner=$2 OR EXISTS(SELECT 1 FROM agent_project_grants g WHERE g.project_id=agent_projects.id AND g.recipient=$2 AND g.expires_at>clock_timestamp()))`

type projectRetryRecord struct {
	Request CollectionRequest
	Attempt string
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
	return p, nil
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
			p.Jobs = append(p.Jobs, AgentProjectJob{ClipID: c.clip, Job: job, Request: c.request, Current: c.current})
		}
	}
	return nil
}

func bindScopedProjectJob(ctx context.Context, tx pgx.Tx, binding projectJobBinding, owner string, job Job) error {
	// Serialize capacity checks with other bindings and recheck the live scope.
	// The same row lock also fences grant revocation and project deletion.
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM agent_projects WHERE id=$1 AND owner=$2 AND source_id=$3 FOR UPDATE`, binding.Scope.ID, owner, job.Request.SourceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// The authorization snapshot must be taken after a possible row-lock wait:
	// a revoker can hold the project lock while deleting the grant.
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_projects WHERE id=$1 AND expires_at>clock_timestamp() AND `+projectAccess+`)`, id, binding.Scope.Actor).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_project_jobs WHERE project_id=$1 AND job_id=$2)`, id, job.ID).Scan(&exists); err != nil || exists {
		return err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_jobs WHERE project_id=$1`, id).Scan(&count); err != nil {
		return err
	}
	if count >= 1024 {
		return errProjectLimit
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_project_jobs(project_id,job_id,clip_id) VALUES($1,$2,$3)`, id, job.ID, binding.Clip)
	return err
}

func (s *Store) createAgentProject(ctx context.Context, id, owner string, body projectCreateRequest, source Source, attached map[string]string, plan, selection, snapshot []byte) (bool, error) {
	created := true
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return created, e
	}
	defer rollbackStorage(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "agent-project:"+owner); e != nil {
		return created, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM agent_projects WHERE id IN (SELECT id FROM agent_projects WHERE expires_at<clock_timestamp() ORDER BY expires_at,id LIMIT 200)`); e != nil {
		return created, e
	}
	var savedSource string
	e = tx.QueryRow(ctx, `SELECT source_id FROM agent_projects WHERE owner=$1 AND collection_key=$2`, owner, body.CollectionKey).Scan(&savedSource)
	if e == nil {
		if savedSource != body.SourceID {
			return created, errProjectConflict
		}
		created = false
		return created, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return created, e
	}
	var n int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_projects WHERE owner=$1`, owner).Scan(&n); e != nil {
		return created, e
	}
	if n >= 20 {
		return created, errProjectLimit
	}
	if _, e = tx.Exec(ctx, `INSERT INTO agent_projects(id,owner,source_id,plan,source_snapshot,clip_ids,rendition,collection_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, owner, source.ID, plan, snapshot, selection, collectionRendition(body.Rendition), body.CollectionKey); e != nil {
		return created, e
	}
	for job, clip := range attached {
		if _, e = tx.Exec(ctx, `INSERT INTO agent_project_jobs(project_id,job_id,clip_id) VALUES($1,$2,$3)`, id, job, clip); e != nil {
			return created, e
		}
	}
	return created, tx.Commit(ctx)
}

func (s *Store) editAgentProject(ctx context.Context, id, actor string, revision int64, plan, selection []byte, rendition string) error {
	tag, e := s.DB.Exec(ctx, `UPDATE agent_projects SET plan=$4,clip_ids=$5,rendition=$6,revision=revision+1 WHERE id=$1 AND `+projectAccess+` AND expires_at>clock_timestamp() AND revision=$3`, id, actor, revision, plan, selection, collectionRendition(rendition))
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errProjectConflict
	}
	return nil
}

func (s *Store) issueProjectHandoff(ctx context.Context, projectID, actor, hash string) (time.Time, error) {
	var expiry time.Time
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return expiry, e
	}
	defer rollbackStorage(tx)
	var owner string
	if e = tx.QueryRow(ctx, `SELECT owner FROM agent_projects WHERE id=$1 AND owner=$2 AND expires_at>clock_timestamp() FOR UPDATE`, projectID, actor).Scan(&owner); errors.Is(e, pgx.ErrNoRows) {
		return expiry, ErrNotFound
	} else if e != nil {
		return expiry, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM agent_project_handoffs WHERE project_id=$1 AND expires_at<clock_timestamp()`, projectID); e != nil {
		return expiry, e
	}
	var n int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_handoffs WHERE project_id=$1`, projectID).Scan(&n); e != nil {
		return expiry, e
	}
	if n >= 8 {
		return expiry, errProjectLimit
	}
	if e = tx.QueryRow(ctx, `INSERT INTO agent_project_handoffs(token_hash,project_id) VALUES($1,$2) RETURNING expires_at`, hash, projectID).Scan(&expiry); e != nil {
		return expiry, e
	}
	return expiry, tx.Commit(ctx)
}

func (s *Store) redeemProjectHandoff(ctx context.Context, actor, hash string) (string, error) {
	var projectID string
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return projectID, e
	}
	defer rollbackStorage(tx)
	var recipient *string
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-recipient:"+actor); e != nil {
		return projectID, e
	}
	if e = tx.QueryRow(ctx, `SELECT h.project_id,h.recipient FROM agent_project_handoffs h JOIN agent_projects p ON p.id=h.project_id WHERE h.token_hash=$1 AND h.expires_at>clock_timestamp() AND p.expires_at>clock_timestamp() FOR UPDATE OF h,p`, hash).Scan(&projectID, &recipient); errors.Is(e, pgx.ErrNoRows) {
		return projectID, ErrNotFound
	} else if e != nil {
		return projectID, e
	}
	if recipient != nil {
		if *recipient != actor {
			return projectID, ErrNotFound
		}
		var exists bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_project_grants WHERE project_id=$1 AND recipient=$2 AND expires_at>clock_timestamp())`, projectID, actor).Scan(&exists); e != nil {
			return projectID, e
		}
		if !exists {
			return projectID, ErrNotFound
		}
		return projectID, tx.Commit(ctx)
	}
	if _, e = tx.Exec(ctx, `DELETE FROM agent_project_grants WHERE expires_at<clock_timestamp() AND (project_id=$1 OR recipient=$2)`, projectID, actor); e != nil {
		return projectID, e
	}
	var n int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_grants WHERE recipient=$1`, actor).Scan(&n); e != nil {
		return projectID, e
	}
	if n >= 20 {
		return projectID, errProjectLimit
	}
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_grants WHERE project_id=$1`, projectID).Scan(&n); e != nil {
		return projectID, e
	}
	if n >= 32 {
		return projectID, errProjectLimit
	}
	if _, e = tx.Exec(ctx, `INSERT INTO agent_project_grants(project_id,recipient,expires_at) VALUES($1,$2,least(clock_timestamp()+interval '24 hours',(SELECT expires_at FROM agent_projects WHERE id=$1))) ON CONFLICT(project_id,recipient) DO NOTHING`, projectID, actor); e != nil {
		return projectID, e
	}
	if _, e = tx.Exec(ctx, `UPDATE agent_project_handoffs SET recipient=$2,redeemed_at=clock_timestamp() WHERE token_hash=$1`, hash, actor); e != nil {
		return projectID, e
	}
	return projectID, tx.Commit(ctx)
}

func (s *Store) revokeAgentProject(ctx context.Context, projectID, actor string, remove bool) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollbackStorage(tx)
	var id string
	if e = tx.QueryRow(ctx, `SELECT id FROM agent_projects WHERE id=$1 AND owner=$2 FOR UPDATE`, projectID, actor).Scan(&id); errors.Is(e, pgx.ErrNoRows) {
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
}

func (s *Store) retryProjectCollection(ctx context.Context, p AgentProject, actor, key, hash, attempt string, expectedRevision int64, clipIDs []string, request CollectionRequest, c Config) (projectRetryRecord, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return projectRetryRecord{Request: request, Attempt: attempt}, e
	}
	defer rollbackStorage(tx)
	var revision int64
	if e = tx.QueryRow(ctx, `SELECT revision FROM agent_projects WHERE id=$1 AND expires_at>clock_timestamp() AND `+projectAccess+` FOR UPDATE`, p.ID, actor).Scan(&revision); errors.Is(e, pgx.ErrNoRows) {
		return projectRetryRecord{Request: request, Attempt: attempt}, ErrNotFound
	} else if e != nil {
		return projectRetryRecord{Request: request, Attempt: attempt}, e
	}
	var stored string
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT intent_hash,request,attempt_key FROM agent_project_retries WHERE project_id=$1 AND key=$2`, p.ID, key).Scan(&stored, &raw, &attempt)
	if e == nil {
		if stored != hash {
			return projectRetryRecord{Request: request, Attempt: attempt}, errProjectConflict
		}
		if e = json.Unmarshal(raw, &request); e != nil {
			return projectRetryRecord{Request: request, Attempt: attempt}, e
		}
		return projectRetryRecord{Request: request, Attempt: attempt}, tx.Commit(ctx)
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return projectRetryRecord{Request: request, Attempt: attempt}, e
	}
	if revision != expectedRevision {
		return projectRetryRecord{Request: request, Attempt: attempt}, errProjectConflict
	}
	allowed, seen := map[string]bool{}, map[string]bool{}
	for _, clip := range p.Clips {
		allowed[clip.ClipID] = true
	}
	for _, id := range clipIDs {
		if !allowed[id] || seen[id] {
			return projectRetryRecord{Request: request, Attempt: attempt}, errProjectSelection
		}
		seen[id] = true
	}
	clips, e := projectPrepared(p.Plan, clipIDs, p.Rendition, p.Source, c, true)
	if e != nil {
		return projectRetryRecord{Request: request, Attempt: attempt}, e
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
			return projectRetryRecord{Request: request, Attempt: attempt}, errProjectConflict
		}
		job, jobErr := readJob(ctx, tx, row.Job.ID, p.Owner)
		if jobErr != nil {
			return projectRetryRecord{Request: request, Attempt: attempt}, jobErr
		}
		if !projectRetryableJob(job, time.Now()) {
			return projectRetryRecord{Request: request, Attempt: attempt}, errProjectConflict
		}
		// The previous intent is durable before its job is admitted. A second
		// caller key must not race that gap and create two repair renders.
		var previousKey string
		e = tx.QueryRow(ctx, `SELECT clip_job_keys->>$2 FROM agent_project_retries WHERE project_id=$1 AND clip_intents->>$2=$3 ORDER BY created_at DESC,key DESC LIMIT 1`, p.ID, clip.ID, collectionJobKey("", clip)).Scan(&previousKey)
		if e == nil {
			var previousID string
			if e = tx.QueryRow(ctx, `SELECT id FROM jobs WHERE owner=$1 AND idempotency_key=$2`, p.Owner, previousKey).Scan(&previousID); errors.Is(e, pgx.ErrNoRows) {
				return projectRetryRecord{Request: request, Attempt: attempt}, errProjectConflict
			} else if e != nil {
				return projectRetryRecord{Request: request, Attempt: attempt}, e
			}
			previous, previousErr := readJob(ctx, tx, previousID, p.Owner)
			if previousErr != nil {
				return projectRetryRecord{Request: request, Attempt: attempt}, previousErr
			}
			if !projectRetryableJob(previous, time.Now()) {
				return projectRetryRecord{Request: request, Attempt: attempt}, errProjectConflict
			}
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return projectRetryRecord{Request: request, Attempt: attempt}, e
		}
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM agent_project_retries WHERE project_id=$1`, p.ID).Scan(&count); e != nil {
		return projectRetryRecord{Request: request, Attempt: attempt}, e
	}
	if count >= 1024 {
		return projectRetryRecord{Request: request, Attempt: attempt}, errProjectLimit
	}
	raw, e = json.Marshal(request)
	if e != nil {
		return projectRetryRecord{Request: request, Attempt: attempt}, e
	}
	clipIntents, clipJobKeys := map[string]string{}, map[string]string{}
	for _, clip := range clips {
		clipIntents[clip.ID] = collectionJobKey("", clip)
		clipJobKeys[clip.ID] = collectionJobKey(attempt, clip)
	}
	intentsJSON, _ := json.Marshal(clipIntents)
	keysJSON, _ := json.Marshal(clipJobKeys)
	if _, e = tx.Exec(ctx, `INSERT INTO agent_project_retries(project_id,key,intent_hash,request,attempt_key,clip_intents,clip_job_keys) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, key, hash, raw, attempt, intentsJSON, keysJSON); e != nil {
		return projectRetryRecord{Request: request, Attempt: attempt}, e
	}
	return projectRetryRecord{Request: request, Attempt: attempt}, tx.Commit(ctx)
}

func (s *Store) bindProjectJob(dbCtx context.Context, binding projectJobBinding, owner string, job Job) error {
	tx, err := s.DB.Begin(dbCtx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	if err = bindScopedProjectJob(dbCtx, tx, binding, owner, job); err != nil {
		return err
	}
	return tx.Commit(dbCtx)
}

func (s *Store) projectResourceAllowed(ctx context.Context, projectID, kind, resourceID, owner string) (bool, error) {
	var yes bool
	query := `SELECT EXISTS(SELECT 1 FROM agent_project_jobs WHERE project_id=$1 AND job_id=$2)`
	if kind == "artifacts" {
		query = `SELECT EXISTS(SELECT 1 FROM agent_project_jobs p JOIN artifacts a ON a.job_id=p.job_id WHERE p.project_id=$1 AND a.id=$2 AND a.owner=$3)`
	}
	var err error
	if kind == "artifacts" {
		err = s.DB.QueryRow(ctx, query, projectID, resourceID, owner).Scan(&yes)
	} else {
		err = s.DB.QueryRow(ctx, query, projectID, resourceID).Scan(&yes)
	}
	return yes, err
}
