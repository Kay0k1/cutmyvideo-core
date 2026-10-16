package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

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
