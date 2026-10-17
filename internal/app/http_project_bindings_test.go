package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func seedProjectBindingHistory(t *testing.T, store *Store, p AgentProject, count int) {
	t.Helper()
	old := ExportRequest{SourceID: p.SourceID, Ranges: []Range{{StartMS: 7000, EndMS: 7500}}, Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "joined"}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB.Exec(context.Background(), `WITH inserted AS (
 INSERT INTO jobs(id,owner,source_id,request,items,status)
 SELECT 'job_history_'||n::text,$1,$2,$3::jsonb,'[]'::jsonb,'succeeded'
 FROM generate_series(1,$5::integer) n RETURNING id
) INSERT INTO agent_project_jobs(project_id,job_id,clip_id) SELECT $4,id,'clip-1' FROM inserted`, p.Owner, p.SourceID, raw, p.ID, count)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPAgentProjectJobLimitRejectsAdmissionAtomicallyAndRetainsReplay(t *testing.T) {
	store, s, ownerCookie, owner, request := remoteCollectionFixture(t, 1)
	p := agentProjectDecode(t, agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "binding-capacity"}, nil), 201)
	p.Owner = owner // The public DTO deliberately omits the owner's capability hash.
	base := "/api/v1/projects/" + p.ID
	issue := agentProjectRequest(t, s, ownerCookie, "POST", base+"/handoffs", struct{}{}, nil)
	var handoff struct {
		Token string `json:"token"`
	}
	if issue.Code != 201 || json.Unmarshal(issue.Body.Bytes(), &handoff) != nil {
		t.Fatal("could not create recipient handoff", issue.Code)
	}
	recipient := agentProjectCookie(123)
	if w := agentProjectRequest(t, s, recipient, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 200 {
		t.Fatal("could not redeem recipient handoff", w.Code)
	}
	seedProjectBindingHistory(t, store, p, 1024)
	counts := func(wantJobs, wantBindings, wantActive int) {
		t.Helper()
		var jobs, bindings, active int
		err := store.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM jobs WHERE owner=$1),(SELECT count(*) FROM agent_project_jobs WHERE project_id=$2),(SELECT count(*) FROM jobs WHERE owner=$1 AND status IN ('queued','running','waiting_storage'))`, owner, p.ID).Scan(&jobs, &bindings, &active)
		if err != nil || jobs != wantJobs || bindings != wantBindings || active != wantActive {
			t.Fatal("project admission changed durable work unexpectedly", jobs, bindings, active, err)
		}
	}
	for _, cookie := range []*http.Cookie{recipient, recipient, ownerCookie} {
		w := agentProjectRequest(t, s, cookie, "POST", base+"/collections", map[string]int64{"revision": p.Revision}, nil)
		report := collectionSubmissionReport(t, w, http.StatusTooManyRequests)
		if report.Complete || report.Clips[0].Error.Code != "project_limit" || report.Clips[0].Retryable {
			t.Fatal("full project did not return a terminal capacity diagnostic", w.Body.String())
		}
		counts(1024, 1024, 0)
	}
	// One free history entry permits one admission. Once full again, both
	// recipients and owners must still recover the already bound job.
	if _, err := store.DB.Exec(context.Background(), `DELETE FROM agent_project_jobs WHERE project_id=$1 AND job_id='job_history_1'`, p.ID); err != nil {
		t.Fatal(err)
	}
	accepted := collectionSubmissionReport(t, agentProjectRequest(t, s, recipient, "POST", base+"/collections", struct{}{}, nil), 202)
	jobID := accepted.Clips[0].Job.ID
	counts(1025, 1024, 1)
	for _, cookie := range []*http.Cookie{recipient, ownerCookie} {
		replayed := collectionSubmissionReport(t, agentProjectRequest(t, s, cookie, "POST", base+"/collections", struct{}{}, nil), 202)
		if replayed.Clips[0].Job.ID != jobID {
			t.Fatal("capacity prevented recovery of the original job")
		}
		counts(1025, 1024, 1)
	}
	if w := agentProjectRequest(t, s, recipient, "GET", base+"/jobs/"+jobID, nil, nil); w.Code != 200 {
		t.Fatal("admitted job was invisible to its recipient", w.Code)
	}
	if w := agentProjectRequest(t, s, recipient, "GET", "/api/v1/jobs/"+jobID, nil, nil); w.Code != 404 {
		t.Fatal("project admission exposed ordinary owner authority", w.Code)
	}
}

func TestHTTPAgentProjectConcurrentAdmissionsShareLastBindingSlot(t *testing.T) {
	store, s, cookie, owner, request := remoteCollectionFixture(t, 2)
	p := agentProjectDecode(t, agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects", projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "last-binding-slot"}, nil), 201)
	p.Owner = owner
	seedProjectBindingHistory(t, store, p, 1023)
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, clip := range request.Plan.Clips {
		go func(id string) {
			responses <- agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects/"+p.ID+"/collections", map[string]any{"revision": p.Revision, "clip_ids": []string{id}}, nil)
		}(clip.ID)
	}
	accepted, refused := 0, 0
	for range 2 {
		w := <-responses
		switch w.Code {
		case 202:
			accepted++
		case 429:
			refused++
			if !strings.Contains(w.Body.String(), `"code":"project_limit"`) {
				t.Fatal("last slot was refused for an unrelated reason", w.Body.String())
			}
		default:
			t.Fatal("unexpected concurrent admission response", w.Code, w.Body.String())
		}
	}
	var jobs, bindings int
	err := store.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM jobs WHERE owner=$1),(SELECT count(*) FROM agent_project_jobs WHERE project_id=$2)`, owner, p.ID).Scan(&jobs, &bindings)
	if err != nil || accepted != 1 || refused != 1 || jobs != 1024 || bindings != 1024 {
		t.Fatal("last project slot admitted invisible or excessive work", accepted, refused, jobs, bindings, err)
	}
}

func TestProjectAdmissionRechecksScopeBeforeCommittingJob(t *testing.T) {
	store, s, cookie, owner, request := remoteCollectionFixture(t, 1)
	p := agentProjectDecode(t, agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects", projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "scope-admission"}, nil), 201)
	prepared, err := projectPrepared(p.Plan, p.ClipIDs, p.Rendition, p.Source, s.Config, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"recipient-without-grant", owner} {
		if actor == owner {
			if _, err = store.DB.Exec(context.Background(), `UPDATE agent_projects SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, p.ID); err != nil {
				t.Fatal(err)
			}
		}
		binding := &projectJobBinding{Scope: projectScope{ID: p.ID, Actor: actor}, Clip: prepared[0].ID}
		_, err = store.createJobLimited(context.Background(), owner, prepared[0].Request, "refused-"+actor, 3, s.Config.MaxActiveJobs, binding)
		if !errors.Is(err, ErrNotFound) {
			t.Fatal("stale project scope admitted a job", err)
		}
		var count int
		if err = store.DB.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE owner=$1`, owner).Scan(&count); err != nil || count != 0 {
			t.Fatal("refused scope retained queued work", count, err)
		}
	}
}

func TestHTTPAgentProjectPartialAdmissionPreservesJobsAtBindingLimit(t *testing.T) {
	store, s, cookie, owner, request := remoteCollectionFixture(t, 2)
	p := agentProjectDecode(t, agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects", projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "partial-binding-limit"}, nil), 201)
	p.Owner = owner
	seedProjectBindingHistory(t, store, p, 1023)
	report := collectionSubmissionReport(t, agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects/"+p.ID+"/collections", struct{}{}, nil), 202)
	if report.Complete || report.Clips[0].Job == nil || report.Clips[1].Error == nil || report.Clips[1].Error.Code != "project_limit" || report.Clips[1].Retryable {
		t.Fatal("capacity refusal discarded an accepted clip", report)
	}
	var jobs, bindings int
	err := store.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM jobs WHERE owner=$1),(SELECT count(*) FROM agent_project_jobs WHERE project_id=$2)`, owner, p.ID).Scan(&jobs, &bindings)
	if err != nil || jobs != 1024 || bindings != 1024 {
		t.Fatal("partial refusal retained unbound work", jobs, bindings, err)
	}
	replayed := collectionSubmissionReport(t, agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects/"+p.ID+"/collections", struct{}{}, nil), 202)
	if replayed.Complete || replayed.Clips[0].Job == nil || replayed.Clips[0].Job.ID != report.Clips[0].Job.ID || replayed.Clips[1].Error == nil || replayed.Clips[1].Error.Code != "project_limit" {
		t.Fatal("partial replay lost the accepted job or capacity diagnostic", replayed)
	}
}

func TestHTTPAgentProjectExplicitBindingsPreserveIdenticalClips(t *testing.T) {
	_, s, ownerCookie, _, request := remoteCollectionFixture(t, 2)
	request.Plan.Clips[1].Ranges = append([]Range(nil), request.Plan.Clips[0].Ranges...)
	request.Plan.Clips[1].Options = request.Plan.Clips[0].Options
	jobs := collectionSubmissionReport(t, httpCollectionRequest(t, s, ownerCookie, "/api/v1/collections", request, "duplicate-cuts"), 202)
	if len(jobs.Clips) != 2 || jobs.Clips[0].Job.ID == jobs.Clips[1].Job.ID {
		t.Fatal("different stable clip IDs lost separate jobs")
	}
	create := projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "explicit-bindings"}
	for _, row := range jobs.Clips {
		create.ClipJobs = append(create.ClipJobs, projectClipJob{ClipID: row.ClipID, JobID: row.Job.ID})
	}
	p := agentProjectDecode(t, agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", create, nil), 201)
	if len(p.Jobs) != 2 {
		t.Fatal("project dropped an identical clip")
	}
	for i, row := range p.Jobs {
		if row.ClipID != request.Plan.Clips[i].ID || row.Job.ID != jobs.Clips[i].Job.ID || !row.Current {
			t.Fatal("project swapped identical clip bindings")
		}
	}
	for name, pairs := range map[string][]projectClipJob{
		"duplicate-clip": {create.ClipJobs[0], create.ClipJobs[0]},
		"duplicate-job":  {create.ClipJobs[0], {ClipID: create.ClipJobs[1].ClipID, JobID: create.ClipJobs[0].JobID}},
		"unknown-clip":   {{ClipID: "unknown", JobID: create.ClipJobs[0].JobID}},
	} {
		bad := create
		bad.CollectionKey = name
		bad.ClipJobs = pairs
		if w := agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", bad, nil); w.Code != 400 {
			t.Fatal("invalid binding accepted", name, w.Code)
		}
	}
	wrong := create
	wrong.CollectionKey = "different-cuts"
	wrong.Plan.Clips = append([]CollectionClip(nil), request.Plan.Clips...)
	wrong.Plan.Clips[0].Ranges = append([]Range(nil), request.Plan.Clips[0].Ranges...)
	wrong.Plan.Clips[0].Ranges[0].StartMS++
	if w := agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", wrong, nil); w.Code != 400 {
		t.Fatal("binding ignored edit identity", w.Code)
	}
	foreign := agentProjectCookie(122)
	if w := agentProjectRequest(t, s, foreign, "POST", "/api/v1/projects", create, nil); w.Code != 404 {
		t.Fatal("binding admitted another owner", w.Code)
	}
}

func TestHTTPAgentProjectExpandedPlanLimitIsCorrectiveAndAtomic(t *testing.T) {
	store, s, cookie, _, request := remoteCollectionFixture(t, 1)
	small := projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "small-original"}
	p := agentProjectDecode(t, agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects", small, nil), 201)
	plan := CollectionPlan{Version: 1, Options: CollectionOptions{Format: "mp4", Quality: "best", CutMode: "accurate"}}
	for i := 0; i < 128; i++ {
		clip := CollectionClip{ID: fmt.Sprintf("clip-%03d", i), Label: strings.Repeat("x", 24)}
		for j := 0; j < 128; j++ {
			clip.Ranges = append(clip.Ranges, Range{StartMS: int64(j), EndMS: int64(j + 1), Label: strings.Repeat("x", 24)})
		}
		plan.Clips = append(plan.Clips, clip)
	}
	large := projectCreateRequest{SourceID: request.SourceID, Plan: plan, CollectionKey: "large-plan"}
	raw, err := json.Marshal(large)
	if err != nil || len(raw) >= 1<<20 {
		t.Fatal("fixture must fit compact HTTP limit", len(raw), err)
	}
	encoded, _ := json.Marshal(plan)
	var storedBytes int
	if err = store.DB.QueryRow(context.Background(), `SELECT octet_length($1::jsonb::text)`, encoded).Scan(&storedBytes); err != nil || storedBytes <= 1<<20 {
		t.Fatal("fixture must exceed expanded JSONB bound", storedBytes, err)
	}
	assertLimit := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"project_too_large"`) || strings.Contains(w.Body.String(), "23514") {
			t.Fatal("size refusal lost corrective private response", w.Code)
		}
	}
	assertLimit(agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects", large, nil))
	assertLimit(agentProjectRequest(t, s, cookie, "PUT", "/api/v1/projects/"+p.ID, projectEditRequest{Revision: p.Revision, Plan: plan}, nil))
	got := agentProjectDecode(t, agentProjectRequest(t, s, cookie, "GET", "/api/v1/projects/"+p.ID, nil, nil), 200)
	if got.Revision != p.Revision || len(got.Plan.Clips) != 1 {
		t.Fatal("failed edit changed retained revision")
	}
	var count int
	if err = store.DB.QueryRow(context.Background(), `SELECT count(*) FROM agent_projects`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed create retained a project", count, err)
	}
}

func TestAgentProjectPersistenceLimitRecognizesOnlyKnownConstraints(t *testing.T) {
	for _, row := range []struct {
		table, name string
		want        int
	}{
		{"agent_projects", "agent_projects_plan_check", 400},
		{"agent_project_retries", "agent_project_retries_request_check", 400},
		{"agent_projects", "unrelated_check", 500},
	} {
		w := httptest.NewRecorder()
		writeProjectPersistenceError(w, fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23514", TableName: row.table, ConstraintName: row.name, Detail: "private SQL detail"}))
		if w.Code != row.want || strings.Contains(w.Body.String(), "private SQL detail") {
			t.Fatal("persistence classification/privacy", row.name, w.Code)
		}
	}
}
