package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func projectLifecycleRecipient(t *testing.T, s *Server, owner *http.Cookie, id string) (*http.Cookie, string) {
	t.Helper()
	w := agentProjectRequest(t, s, owner, "POST", "/api/v1/projects/"+id+"/handoffs", struct{}{}, nil)
	var handoff struct {
		Token string `json:"token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &handoff) != nil || handoff.Token == "" {
		t.Fatal("could not issue lifecycle handoff", w.Code)
	}
	recipient := agentProjectCookie(161)
	if w = agentProjectRequest(t, s, recipient, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 200 {
		t.Fatal("could not redeem lifecycle handoff", w.Code)
	}
	return recipient, handoff.Token
}

func projectLifecycleRows(t *testing.T, store *Store, id string, want [4]int) {
	t.Helper()
	var got [4]int
	err := store.DB.QueryRow(context.Background(), `SELECT
 (SELECT count(*) FROM agent_projects WHERE id=$1),
 (SELECT count(*) FROM agent_project_grants WHERE project_id=$1),
 (SELECT count(*) FROM agent_project_handoffs WHERE project_id=$1),
 (SELECT count(*) FROM agent_project_jobs WHERE project_id=$1)`, id).Scan(&got[0], &got[1], &got[2], &got[3])
	if err != nil || got != want {
		t.Fatal("unexpected project/grant/handoff/job-link retention", got, want, err)
	}
}

func TestProjectLifecycleRevokeAndDeletePreserveAcceptedWork(t *testing.T) {
	store, s, ownerCookie, owner, request := remoteCollectionFixture(t, 2)
	s.Config.ArtifactTTL = time.Hour
	store.ConfigureStorage(s.Config)
	submission := collectionSubmissionReport(t, httpCollectionRequest(t, s, ownerCookie, "/api/v1/collections", request, "lifecycle-work"), 202)
	ready, token, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: newID("artifact"), Filename: "retained.mp4", SizeBytes: 19}
	path := writeLedgerFile(t, s.Config, "artifacts", artifact.ID+".mp4", 19)
	if err = store.AddArtifact(context.Background(), owner, ready.ID, path, token, artifact); err != nil {
		t.Fatal(err)
	}
	ready.Status, ready.Stage = "succeeded", "finished"
	ready.Items[0].Status, ready.Items[0].Artifact = "succeeded", &artifact
	if err = store.SaveJob(context.Background(), ready, token); err != nil {
		t.Fatal(err)
	}
	create := projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "lifecycle-project"}
	pending := ""
	for _, clip := range submission.Clips {
		create.ClipJobs = append(create.ClipJobs, projectClipJob{ClipID: clip.ClipID, JobID: clip.Job.ID})
		if clip.Job.ID != ready.ID {
			pending = clip.Job.ID
		}
	}
	p := agentProjectDecode(t, agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", create, nil), 201)
	base := "/api/v1/projects/" + p.ID
	recipient, _ := projectLifecycleRecipient(t, s, ownerCookie, p.ID)
	projectLifecycleRows(t, store, p.ID, [4]int{1, 1, 1, 2})
	assertWork := func() {
		t.Helper()
		queued, e := store.Job(context.Background(), pending, owner)
		if e != nil || queued.Status != "queued" || queued.Cancelled {
			t.Fatal("project access change altered accepted pending work", e)
		}
		completed, e := store.Job(context.Background(), ready.ID, owner)
		if e != nil || completed.Status != "succeeded" || completed.Cancelled || completed.Items[0].Artifact == nil || completed.Items[0].Artifact.ID != artifact.ID {
			t.Fatal("project access change altered the completed result", e)
		}
		for _, id := range []string{ready.ID, pending} {
			if w := agentProjectRequest(t, s, ownerCookie, "GET", "/api/v1/jobs/"+id, nil, nil); w.Code != 200 {
				t.Fatal("project access change removed the owner's job", w.Code)
			}
		}
		w := agentProjectRequest(t, s, ownerCookie, "GET", "/api/v1/artifacts/"+artifact.ID+"/download", nil, nil)
		if w.Code != 200 || w.Body.Len() != 19 {
			t.Fatal("project access change removed the owner's artifact", w.Code)
		}
		if data, e := os.ReadFile(path); e != nil || len(data) != 19 {
			t.Fatal("project access change removed persistent media", e)
		}
	}
	if w := agentProjectRequest(t, s, ownerCookie, "DELETE", base+"/grants", nil, nil); w.Code != 200 {
		t.Fatal("grant revocation failed", w.Code)
	}
	projectLifecycleRows(t, store, p.ID, [4]int{1, 0, 0, 2})
	if w := agentProjectRequest(t, s, recipient, "GET", base+"/artifacts/"+artifact.ID+"/download", nil, nil); w.Code != 404 {
		t.Fatal("revoked recipient retained artifact access", w.Code)
	}
	assertWork()
	if w := agentProjectRequest(t, s, ownerCookie, "DELETE", base, nil, nil); w.Code != 200 {
		t.Fatal("project deletion failed", w.Code)
	}
	projectLifecycleRows(t, store, p.ID, [4]int{})
	assertWork()
	claimed, _, err := store.Claim(context.Background())
	if err != nil || claimed.ID != pending {
		t.Fatal("project deletion stopped accepted queue work", claimed.ID, err)
	}
}

func TestProjectLifecycleAccessExpiryPrecedesAdmissionCleanup(t *testing.T) {
	store, s, ownerCookie, _, request := remoteCollectionFixture(t, 1)
	s.Config.SourceTTL, s.Config.ArtifactTTL = 24*time.Hour, time.Hour
	store.ConfigureStorage(s.Config)
	create := projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "access-expiry"}
	p := agentProjectDecode(t, agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", create, nil), 201)
	// Use a short remaining project lifetime to exercise the grant's deadline cap.
	if _, err := store.DB.Exec(context.Background(), `UPDATE agent_projects SET expires_at=clock_timestamp()+interval '2 hours' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	recipient, _ := projectLifecycleRecipient(t, s, ownerCookie, p.ID)
	base := "/api/v1/projects/" + p.ID
	shared := agentProjectDecode(t, agentProjectRequest(t, s, recipient, "GET", base, nil, nil), 200)
	if shared.AccessExpiresAt == nil || !shared.AccessExpiresAt.Equal(shared.ExpiresAt) {
		t.Fatal("recipient grant outlived its project")
	}
	if _, err := store.DB.Exec(context.Background(), `UPDATE agent_project_handoffs SET expires_at=clock_timestamp()-interval '1 second' WHERE project_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if w := agentProjectRequest(t, s, recipient, "GET", base, nil, nil); w.Code != 200 {
		t.Fatal("token expiry revoked an already issued grant", w.Code)
	}
	if _, err := store.DB.Exec(context.Background(), `UPDATE agent_project_grants SET expires_at=clock_timestamp()-interval '1 second' WHERE project_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if w := agentProjectRequest(t, s, recipient, "GET", base, nil, nil); w.Code != 404 {
		t.Fatal("expired grant retained project access", w.Code)
	}
	if w := agentProjectRequest(t, s, ownerCookie, "GET", base, nil, nil); w.Code != 200 {
		t.Fatal("recipient grant expiry hid the project from its owner", w.Code)
	}
	projectLifecycleRows(t, store, p.ID, [4]int{1, 1, 1, 0})
	// Even a stale grant row with a later deadline cannot extend project access.
	if _, err := store.DB.Exec(context.Background(), `UPDATE agent_project_grants SET expires_at=clock_timestamp()+interval '1 hour' WHERE project_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(context.Background(), `UPDATE agent_projects SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []*http.Cookie{ownerCookie, recipient} {
		if w := agentProjectRequest(t, s, cookie, "GET", base, nil, nil); w.Code != 404 {
			t.Fatal("expired project remained visible", w.Code)
		}
	}
	if _, err := store.Cleanup(context.Background(), s.Config.ArtifactTTL, s.Config.SourceTTL); err != nil {
		t.Fatal(err)
	}
	projectLifecycleRows(t, store, p.ID, [4]int{1, 1, 1, 0})
	create.CollectionKey = "later-project-admission"
	agentProjectDecode(t, agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", create, nil), 201)
	projectLifecycleRows(t, store, p.ID, [4]int{})
	if w := agentProjectRequest(t, s, ownerCookie, "GET", "/api/v1/sources/"+request.SourceID, nil, nil); w.Code != 200 {
		t.Fatal("project admission cleanup removed its source", w.Code)
	}
}

func TestProjectLifecycleLiveProjectAndGrantDoNotPinSource(t *testing.T) {
	store, s, ownerCookie, owner, request := remoteCollectionFixture(t, 1)
	s.Config.SourceTTL, s.Config.ArtifactTTL = time.Hour, time.Hour
	store.ConfigureStorage(s.Config)
	p := agentProjectDecode(t, agentProjectRequest(t, s, ownerCookie, "POST", "/api/v1/projects", projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "source-expiry"}, nil), 201)
	recipient, _ := projectLifecycleRecipient(t, s, ownerCookie, p.ID)
	if _, err := store.DB.Exec(context.Background(), `UPDATE sources SET created_at=clock_timestamp()-interval '2 hours' WHERE id=$1 AND owner=$2`, request.SourceID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(context.Background(), s.Config.ArtifactTTL, s.Config.SourceTTL); err != nil {
		t.Fatal(err)
	}
	if w := agentProjectRequest(t, s, ownerCookie, "GET", "/api/v1/sources/"+request.SourceID, nil, nil); w.Code != 404 {
		t.Fatal("project or grant pinned an otherwise expired source", w.Code)
	}
	base := "/api/v1/projects/" + p.ID
	for _, cookie := range []*http.Cookie{ownerCookie, recipient} {
		recovered := agentProjectDecode(t, agentProjectRequest(t, s, cookie, "GET", base, nil, nil), 200)
		if recovered.SourceID != request.SourceID || !recovered.Source.MediaRemoved || len(recovered.Plan.Clips) != 1 || recovered.Source.PreviewURL != nil {
			t.Fatal("source expiry discarded the saved project or offered missing bytes")
		}
		if w := agentProjectRequest(t, s, cookie, "POST", base+"/collections", struct{}{}, nil); w.Code != 404 {
			t.Fatal("saved project silently recreated an expired source", w.Code)
		}
	}
	projectLifecycleRows(t, store, p.ID, [4]int{1, 1, 1, 0})
}
