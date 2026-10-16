package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func agentProjectRequest(t *testing.T, s *Server, cookie *http.Cookie, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, "https://example.com"+path, nil)
	} else {
		r = httptest.NewRequest(method, "https://example.com"+path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestHTTPAgentProjectPublicContract(t *testing.T) {
	python := os.Getenv("CUTMY_CONTRACT_PYTHON")
	if python == "" {
		python = "python3"
	}
	if err := exec.Command(python, "-c", "import yaml,jsonschema").Run(); err != nil {
		if os.Getenv("CUTMY_REQUIRE_CONTRACT") == "1" {
			t.Fatal("contract checker dependencies required")
		}
		t.Skip("contract checker dependencies absent")
	}
	store, s, a, owner, request := remoteCollectionFixture(t, 1)
	var samples []map[string]any
	record := func(name, method, route, path string, body any, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		w := agentProjectRequest(t, s, cookie, method, path, body, nil)
		if w.Code != status {
			t.Fatal("project contract unexpected status", name, w.Code)
		}
		headers := map[string]string{}
		for key := range w.Header() {
			headers[key] = w.Header().Get(key)
		}
		var response any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		sample := map[string]any{"name": name, "method": method, "route": route, "status": w.Code, "headers": headers, "body": response}
		if body != nil {
			sample["request"] = body
		}
		samples = append(samples, sample)
		return w
	}
	create := projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "contract-workflow"}
	w := record("create", "POST", "/api/v1/projects", "/api/v1/projects", create, a, 201)
	p := agentProjectDecode(t, w, 201)
	base := "/api/v1/projects/" + p.ID
	record("creation-replay", "POST", "/api/v1/projects", "/api/v1/projects", create, a, 200)
	record("project", "GET", "/api/v1/projects/{project}", base, nil, a, 200)
	p = agentProjectDecode(t, record("project-edit", "PUT", "/api/v1/projects/{project}", base, projectEditRequest{Revision: p.Revision, Plan: p.Plan}, a, 200), 200)
	record("project-validate", "POST", "/api/v1/projects/{project}/collections/validate", base+"/collections/validate", map[string]any{"revision": p.Revision, "clip_ids": []string{request.Plan.Clips[0].ID}}, a, 200)
	submission := record("project-submit", "POST", "/api/v1/projects/{project}/collections", base+"/collections", struct{}{}, a, 202)
	var submitted CollectionSubmission
	if err := json.Unmarshal(submission.Body.Bytes(), &submitted); err != nil || len(submitted.Clips) != 1 || submitted.Clips[0].Job == nil {
		t.Fatal("no explicit job for contract fixture")
	}
	explicit := create
	explicit.CollectionKey = "contract-explicit-bindings"
	explicit.ClipJobs = []projectClipJob{{ClipID: submitted.Clips[0].ClipID, JobID: submitted.Clips[0].Job.ID}}
	record("explicit-clip-bindings", "POST", "/api/v1/projects", "/api/v1/projects", explicit, a, 201)
	record("project-jobs", "GET", "/api/v1/projects/{project}", base, nil, a, 200)
	issue := record("handoff", "POST", "/api/v1/projects/{project}/handoffs", base+"/handoffs", struct{}{}, a, 201)
	var handoff struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(issue.Body.Bytes(), &handoff); err != nil {
		t.Fatal(err)
	}
	b := agentProjectCookie(91)
	record("redeem", "POST", "/api/v1/projects/handoffs/redeem", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, b, 200)
	record("scoped-source", "GET", "/api/v1/projects/{project}/sources/{id}", base+"/sources/"+request.SourceID, nil, b, 200)
	record("revoke", "DELETE", "/api/v1/projects/{project}/grants", base+"/grants", nil, a, 200)
	intent := sourcePreparationIntent{Kind: "import", URL: "https://media.example/recording.mp4"}
	claim, err := store.claimSourcePreparation(context.Background(), owner, "contract-source-key", intent, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{ID: claim.sourceID, Owner: owner, Kind: "platform", Title: "Contract source", DurationMS: 4000, Fingerprint: &SourceFingerprint{Kind: "provider", CanonicalURLSHA256: strings.Repeat("a", 64)}}
	claim.attach(&source)
	if err = store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	record("source-preparation", "GET", "/api/v1/source-preparations/{key}", "/api/v1/source-preparations/contract-source-key", nil, a, 200)
	data, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent-project-contract.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = exec.Command(python, filepath.Join("..", "..", "scripts", "check-contract.py"), "--samples", path).Run(); err != nil {
		t.Fatal("actual project/source-preparation responses failed OpenAPI validation")
	}
}

func TestHTTPAgentProjectCreationRecoveryAndConcurrentHandoff(t *testing.T) {
	store, s, a, owner, request := remoteCollectionFixture(t, 1)
	create := projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "durable-project-key"}
	responses := make(chan *httptest.ResponseRecorder, 4)
	var group sync.WaitGroup
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- agentProjectRequest(t, s, a, "POST", "/api/v1/projects", create, nil)
		}()
	}
	group.Wait()
	close(responses)
	var p AgentProject
	created := 0
	for w := range responses {
		if w.Code == 201 {
			created++
		} else if w.Code != 200 {
			t.Fatal("concurrent project creation failed", w.Code)
		}
		next := agentProjectDecode(t, w, w.Code)
		if p.ID != "" && next.ID != p.ID {
			t.Fatal("same workflow created multiple projects")
		}
		p = next
	}
	if created != 1 {
		t.Fatal("concurrent project creation did not have one durable winner", created)
	}
	base := "/api/v1/projects/" + p.ID
	edited := p.Plan
	edited.Clips = append([]CollectionClip(nil), p.Plan.Clips...)
	edited.Clips[0].Label = "Browser revision"
	p = agentProjectDecode(t, agentProjectRequest(t, s, a, "PUT", base, projectEditRequest{Revision: p.Revision, Plan: edited}, nil), 200)
	if _, err := store.DB.Exec(context.Background(), `DELETE FROM sources WHERE id=$1 AND owner=$2`, request.SourceID, owner); err != nil {
		t.Fatal(err)
	}
	s.Config.FFprobe = "/must-not-launch"
	s.Config.MaxRanges = 1
	recovered := agentProjectDecode(t, agentProjectRequest(t, s, a, "POST", "/api/v1/projects", create, nil), 200)
	if recovered.ID != p.ID || recovered.Revision != p.Revision || recovered.Plan.Clips[0].Label != "Browser revision" || !recovered.Source.MediaRemoved {
		t.Fatal("lost creation reply failed to recover current edits after source cleanup")
	}
	conflict := create
	conflict.SourceID = "different-source"
	if w := agentProjectRequest(t, s, a, "POST", "/api/v1/projects", conflict, nil); w.Code != 409 {
		t.Fatal("workflow key changed fixed source", w.Code)
	}
	issue := agentProjectRequest(t, s, a, "POST", base+"/handoffs", struct{}{}, nil)
	var token struct {
		Token string `json:"token"`
	}
	if issue.Code != 201 || json.Unmarshal(issue.Body.Bytes(), &token) != nil {
		t.Fatal("handoff preparation failed")
	}
	winners := make(chan int, 2)
	for _, b := range []*http.Cookie{agentProjectCookie(61), agentProjectCookie(62)} {
		group.Add(1)
		go func(cookie *http.Cookie) {
			defer group.Done()
			winners <- agentProjectRequest(t, s, cookie, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": token.Token}, nil).Code
		}(b)
	}
	group.Wait()
	close(winners)
	success, refused := 0, 0
	for code := range winners {
		if code == 200 {
			success++
		} else if code == 404 {
			refused++
		} else {
			t.Fatal("concurrent redeem failed", code)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatal("handoff was not single-recipient under concurrency")
	}
	var count int
	if err := store.DB.QueryRow(context.Background(), `SELECT count(*) FROM agent_projects WHERE owner=$1`, owner).Scan(&count); err != nil || count != 1 {
		t.Fatal("creation recovery duplicated project metadata", count, err)
	}
}

func TestHTTPAgentProjectAttachedArtifactsAndRange(t *testing.T) {
	store, s, a, owner, request := remoteCollectionFixture(t, 2)
	submitted := collectionSubmissionReport(t, httpCollectionRequest(t, s, a, "/api/v1/collections", request, "artifact-scope-key"), 202)
	artifacts := map[string]Artifact{}
	for range 2 {
		job, token, err := store.Claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		artifact := Artifact{ID: newID("artifact"), Filename: "clip.mp4", SizeBytes: 19}
		path := writeLedgerFile(t, s.Config, "outputs", artifact.ID+".mp4", 19)
		if err = store.AddArtifact(context.Background(), owner, job.ID, path, token, artifact); err != nil {
			t.Fatal(err)
		}
		job.Status, job.Stage = "succeeded", "finished"
		job.Items[0].Status = "succeeded"
		job.Items[0].Artifact = &artifact
		if err = store.SaveJob(context.Background(), job, token); err != nil {
			t.Fatal(err)
		}
		artifacts[job.ID] = artifact
	}
	attachedID := submitted.Clips[0].Job.ID
	attached, foreign := artifacts[attachedID], artifacts[submitted.Clips[1].Job.ID]
	p := agentProjectDecode(t, agentProjectRequest(t, s, a, "POST", "/api/v1/projects", projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "artifact-project-key", JobIDs: []string{attachedID}}, nil), 201)
	base := "/api/v1/projects/" + p.ID
	b := agentProjectCookie(71)
	issue := agentProjectRequest(t, s, a, "POST", base+"/handoffs", struct{}{}, nil)
	var handoff struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(issue.Body.Bytes(), &handoff) != nil || issue.Code != 201 {
		t.Fatal("handoff issue failed")
	}
	if w := agentProjectRequest(t, s, b, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 200 {
		t.Fatal("handoff redeem failed")
	}
	jobResponse := agentProjectRequest(t, s, b, "GET", base+"/jobs/"+attachedID, nil, nil)
	if jobResponse.Code != 200 || !strings.Contains(jobResponse.Body.String(), base+"/artifacts/"+attached.ID+"/download") {
		t.Fatal("scoped job exposed a global artifact URL")
	}
	w := agentProjectRequest(t, s, b, "GET", base+"/artifacts/"+attached.ID+"/download", nil, map[string]string{"Range": "bytes=2-8"})
	if w.Code != 206 || len(w.Body.Bytes()) != 7 || w.Header().Get("Content-Range") != "bytes 2-8/19" {
		t.Fatal("scoped artifact lost real range serving", w.Code)
	}
	zipResponse := agentProjectRequest(t, s, b, "GET", base+"/jobs/"+attachedID+"/download.zip", nil, nil)
	z, err := zip.NewReader(bytes.NewReader(zipResponse.Body.Bytes()), int64(zipResponse.Body.Len()))
	if zipResponse.Code != 200 || err != nil || len(z.File) != 1 {
		t.Fatal("scoped ZIP did not contain only attached output", zipResponse.Code, err)
	}
	f, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || len(data) != 19 {
		t.Fatal("scoped ZIP changed attached artifact bytes", err)
	}
	for _, path := range []string{base + "/artifacts/" + foreign.ID + "/download", base + "/jobs/" + submitted.Clips[1].Job.ID + "/download.zip", base + "/jobs/" + attachedID + "/download.zip?artifact_id=" + foreign.ID} {
		if w := agentProjectRequest(t, s, b, "GET", path, nil, nil); w.Code != 404 && w.Code != 400 {
			t.Fatal("ZIP or direct download escaped explicit project jobs", w.Code)
		}
	}
	if w := agentProjectRequest(t, s, b, "POST", base+"/collections", map[string]any{"revision": p.Revision - 1}, nil); w.Code != 409 {
		t.Fatal("scoped SDK submission ignored stale revision")
	}
	if w := agentProjectRequest(t, s, b, "POST", base+"/collections", map[string]any{"revision": p.Revision, "clip_ids": []string{"foreign"}}, nil); w.Code != 400 {
		t.Fatal("scoped SDK submission escaped current selection")
	}
	for _, ids := range [][]string{{"foreign"}, {request.Plan.Clips[0].ID, request.Plan.Clips[0].ID}} {
		if w := agentProjectRequest(t, s, b, "POST", base+"/collections/retry", map[string]any{"revision": p.Revision, "clip_ids": ids}, map[string]string{"Idempotency-Key": "invalid-retry-selection"}); w.Code != 400 {
			t.Fatal("retry did not reject invalid selection as a request error", w.Code)
		}
	}
	if w := agentProjectRequest(t, s, b, "POST", base+"/collections/retry", map[string]any{"revision": p.Revision, "clip_ids": []string{request.Plan.Clips[0].ID}}, map[string]string{"Idempotency-Key": "no-ready-retry"}); w.Code != 409 {
		t.Fatal("ready artifact was rerendered as expired")
	}
	if _, err = store.DB.Exec(context.Background(), `DELETE FROM artifacts WHERE id=$1`, attached.ID); err != nil {
		t.Fatal(err)
	}
	retryBody := map[string]any{"revision": p.Revision, "clip_ids": []string{request.Plan.Clips[0].ID}}
	if _, err = store.DB.Exec(context.Background(), `UPDATE sources SET media_removed=true WHERE id=$1`, request.SourceID); err != nil {
		t.Fatal(err)
	}
	if w := agentProjectRequest(t, s, b, "POST", base+"/collections/retry", retryBody, map[string]string{"Idempotency-Key": "explicit-expired-retry"}); w.Code != 410 {
		t.Fatal("pending retry did not retain actual source media guard", w.Code)
	}
	if _, err = store.DB.Exec(context.Background(), `UPDATE sources SET media_removed=false WHERE id=$1`, request.SourceID); err != nil {
		t.Fatal(err)
	}
	if w := agentProjectRequest(t, s, b, "POST", base+"/collections/retry", retryBody, map[string]string{"Idempotency-Key": "replacement-for-unresolved-key"}); w.Code != 409 {
		t.Fatal("new retry key bypassed a durable unadmitted attempt", w.Code)
	}
	result := collectionSubmissionReport(t, agentProjectRequest(t, s, b, "POST", base+"/collections/retry", retryBody, map[string]string{"Idempotency-Key": "explicit-expired-retry"}), 202)
	if result.Clips[0].Job.ID == attachedID {
		t.Fatal("expired completed artifact did not start a distinct explicit attempt")
	}
	if _, err = store.DB.Exec(context.Background(), `UPDATE jobs SET status='failed' WHERE id=$1`, result.Clips[0].Job.ID); err != nil {
		t.Fatal(err)
	}
	responses := make(chan int, 2)
	var group sync.WaitGroup
	for _, key := range []string{"concurrent-repair-a", "concurrent-repair-b"} {
		group.Add(1)
		go func(key string) {
			defer group.Done()
			responses <- agentProjectRequest(t, s, b, "POST", base+"/collections/retry", retryBody, map[string]string{"Idempotency-Key": key}).Code
		}(key)
	}
	group.Wait()
	close(responses)
	accepted, conflict := 0, 0
	for code := range responses {
		if code == 202 {
			accepted++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatal("concurrent repair returned unexpected status", code)
		}
	}
	if accepted != 1 || conflict != 1 {
		t.Fatal("two caller keys created concurrent attempts for the same failed clip", accepted, conflict)
	}
	var count int
	if err = store.DB.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE owner=$1`, owner).Scan(&count); err != nil || count != 4 {
		t.Fatal("repair concurrency created duplicate jobs", count, err)
	}
}
func agentProjectCookie(value byte) *http.Cookie {
	return &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))}
}
func agentProjectDecode(t *testing.T, w *httptest.ResponseRecorder, status int) AgentProject {
	t.Helper()
	if w.Code != status {
		t.Fatalf("project response status=%d, want=%d", w.Code, status)
	}
	var p AgentProject
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.ID == "" || p.Version != 1 {
		t.Fatal("invalid project response", err)
	}
	return p
}

func TestHTTPAgentProjectHandoffIsolationEditsAndRepair(t *testing.T) {
	store, server, a, owner, request := remoteCollectionFixture(t, 2)
	ctx := context.Background()
	rootKey := "private-collection-workflow-key"
	initial := collectionSubmissionReport(t, httpCollectionRequest(t, server, a, "/api/v1/collections", request, rootKey), 202)
	failedID := initial.Clips[1].Job.ID
	if _, err := store.DB.Exec(ctx, `UPDATE jobs SET status='failed',items=(SELECT jsonb_agg(item||'{"status":"failed"}'::jsonb) FROM jsonb_array_elements(items) item) WHERE id=$1`, failedID); err != nil {
		t.Fatal(err)
	}
	selected := request
	selected.ClipIDs = []string{request.Plan.Clips[1].ID}
	repaired := collectionSubmissionReport(t, httpCollectionRequest(t, server, a, "/api/v1/collections", selected, "private-sdk-attempt-key"), 202)
	repairedID := repaired.Clips[0].Job.ID
	if _, err := store.DB.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE id=$1`, repairedID); err != nil {
		t.Fatal(err)
	}
	create := projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: rootKey, JobIDs: []string{initial.Clips[0].Job.ID, repairedID}}
	p := agentProjectDecode(t, agentProjectRequest(t, server, a, "POST", "/api/v1/projects", create, nil), 201)
	base := "/api/v1/projects/" + p.ID
	if len(p.Clips) != 2 || len(p.Jobs) != 2 || !p.Jobs[1].Current || p.Jobs[1].Job.ID != repairedID {
		t.Fatal("handoff lost the current explicit SDK retry")
	}
	issue := agentProjectRequest(t, server, a, "POST", base+"/handoffs", struct{}{}, nil)
	if issue.Code != 201 {
		t.Fatal("could not create scoped handoff", issue.Code)
	}
	var handoff struct {
		Token     string `json:"token"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(issue.Body.Bytes(), &handoff); err != nil || len(handoff.Token) != 43 || handoff.ProjectID != p.ID {
		t.Fatal("invalid narrow handoff response")
	}
	b, c := agentProjectCookie(21), agentProjectCookie(22)
	redeem := agentProjectRequest(t, server, b, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil)
	if redeem.Code != 200 {
		t.Fatal("handoff redemption failed", redeem.Code)
	}
	if strings.Contains(redeem.Body.String(), rootKey) || strings.Contains(redeem.Body.String(), a.Value) || strings.Contains(redeem.Body.String(), "collection_key") {
		t.Fatal("scoped project leaked creator authority")
	}
	if w := agentProjectRequest(t, server, b, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 200 {
		t.Fatal("lost redemption reply cannot recover for the same recipient")
	}
	if w := agentProjectRequest(t, server, c, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 404 {
		t.Fatal("handoff token had multiple recipients")
	}
	for _, path := range []string{"/api/v1/sources/" + request.SourceID, "/api/v1/jobs/" + repairedID, base + "/sources/unrelated-source", base + "/jobs/" + failedID, base + "/jobs/" + failedID + "/download.zip"} {
		if w := agentProjectRequest(t, server, b, "GET", path, nil, nil); w.Code != 404 {
			t.Fatal("grant escaped selected source/job scope", path, w.Code)
		}
	}
	if w := agentProjectRequest(t, server, b, "GET", base+"/sources/"+request.SourceID+"?owner=other", nil, nil); w.Code != 400 {
		t.Fatal("resource query overrode the project scope")
	}
	before := collectionSubmissionReport(t, agentProjectRequest(t, server, b, "POST", base+"/collections", struct{}{}, nil), 202)
	if before.Clips[1].Job.ID != repairedID {
		t.Fatal("browser replay resurrected the old failed root-key attempt")
	}
	var count int
	if err := store.DB.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE owner=$1`, owner).Scan(&count); err != nil || count != 3 {
		t.Fatal("project replay rendered duplicate clips", count, err)
	}
	edited := request.Plan
	edited.Clips = append([]CollectionClip(nil), request.Plan.Clips...)
	edited.Clips[0].Ranges = append([]Range(nil), edited.Clips[0].Ranges...)
	edited.Clips[0].Ranges[0].EndMS += 100
	save := projectEditRequest{Revision: p.Revision, Plan: edited, Rendition: "final"}
	if w := agentProjectRequest(t, server, b, "PUT", base, save, map[string]string{"Origin": "https://foreign.example"}); w.Code != 403 {
		t.Fatal("cross-origin project edit bypassed CSRF checks")
	}
	p = agentProjectDecode(t, agentProjectRequest(t, server, b, "PUT", base, save, nil), 200)
	if w := agentProjectRequest(t, server, b, "PUT", base, save, nil); w.Code != 409 {
		t.Fatal("stale project revision overwrote newer edits")
	}
	changed := collectionSubmissionReport(t, agentProjectRequest(t, server, b, "POST", base+"/collections", struct{}{}, nil), 202)
	if changed.Clips[0].Job.ID == initial.Clips[0].Job.ID || changed.Clips[1].Job.ID != repairedID {
		t.Fatal("selected project edit did not retain the other SDK retry")
	}
	if _, err := store.DB.Exec(ctx, `UPDATE jobs SET status='failed',items=(SELECT jsonb_agg(item||'{"status":"failed"}'::jsonb) FROM jsonb_array_elements(items) item) WHERE id=$1`, changed.Clips[0].Job.ID); err != nil {
		t.Fatal(err)
	}
	retryBody := map[string]any{"revision": p.Revision, "clip_ids": []string{request.Plan.Clips[0].ID}}
	retryHeaders := map[string]string{"Idempotency-Key": "saved-web-repair-attempt"}
	retry := collectionSubmissionReport(t, agentProjectRequest(t, server, b, "POST", base+"/collections/retry", retryBody, retryHeaders), 202)
	retryAgain := collectionSubmissionReport(t, agentProjectRequest(t, server, b, "POST", base+"/collections/retry", retryBody, retryHeaders), 202)
	if retry.Clips[0].Job.ID == changed.Clips[0].Job.ID || retryAgain.Clips[0].Job.ID != retry.Clips[0].Job.ID {
		t.Fatal("web retry lost its distinct durable attempt identity")
	}
	if w := agentProjectRequest(t, server, b, "POST", base+"/collections/retry", retryBody, map[string]string{"Idempotency-Key": "another-attempt"}); w.Code != 409 {
		t.Fatal("a queued/successful clip was retried as a failed clip")
	}
	p = agentProjectDecode(t, agentProjectRequest(t, server, a, "GET", base, nil, nil), 200)
	if len(p.Clips) != 2 || !p.Jobs[0].Current || p.Jobs[0].Job.ID != retry.Clips[0].Job.ID || p.Jobs[1].Job.ID != repairedID {
		t.Fatal("creator cannot adopt exact current browser jobs")
	}
	if err := store.DB.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE owner=$1`, owner).Scan(&count); err != nil || count != 5 {
		t.Fatal("retry or edit created extra renders", count, err)
	}
	if w := agentProjectRequest(t, server, b, "DELETE", base+"/grants", nil, nil); w.Code != 404 {
		t.Fatal("recipient could revoke creator project authority")
	}
	if w := agentProjectRequest(t, server, a, "DELETE", base+"/grants", nil, nil); w.Code != 200 {
		t.Fatal("creator revocation failed")
	}
	if w := agentProjectRequest(t, server, b, "GET", base, nil, nil); w.Code != 404 {
		t.Fatal("revoked project grant remained usable")
	}
	if w := agentProjectRequest(t, server, b, "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": handoff.Token}, nil); w.Code != 404 {
		t.Fatal("revoked handoff restored a grant")
	}
}

func TestHTTPAgentProjectExpiryAndPutMutationAdmission(t *testing.T) {
	store, s, a, _, request := remoteCollectionFixture(t, 1)
	p := agentProjectDecode(t, agentProjectRequest(t, s, a, "POST", "/api/v1/projects", projectCreateRequest{SourceID: request.SourceID, Plan: request.Plan, CollectionKey: "scope-key"}, nil), 201)
	base := "/api/v1/projects/" + p.ID
	issue := agentProjectRequest(t, s, a, "POST", base+"/handoffs", struct{}{}, nil)
	var body struct {
		Token string `json:"token"`
	}
	if issue.Code != 201 || json.Unmarshal(issue.Body.Bytes(), &body) != nil {
		t.Fatal("handoff preparation failed")
	}
	if _, err := store.DB.Exec(context.Background(), `UPDATE agent_project_handoffs SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if w := agentProjectRequest(t, s, agentProjectCookie(31), "POST", "/api/v1/projects/handoffs/redeem", map[string]string{"token": body.Token}, nil); w.Code != 404 {
		t.Fatal("expired handoff was accepted")
	}
	s.rate = map[string]*rateEntry{}
	s.Config.MutationsPerMinute = 1
	p = agentProjectDecode(t, agentProjectRequest(t, s, a, "PUT", base, projectEditRequest{Revision: p.Revision, Plan: p.Plan}, nil), 200)
	if w := agentProjectRequest(t, s, a, "PUT", base, projectEditRequest{Revision: p.Revision, Plan: p.Plan}, nil); w.Code != 429 {
		t.Fatal("PUT bypassed per-IP mutation admission")
	}
}
