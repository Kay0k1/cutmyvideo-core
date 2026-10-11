package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJobHistoryIsPrivateBoundedAndKeepsActiveJobs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	r := httptest.NewRequest("GET", "/api/v1/jobs", nil)
	r.AddCookie(cookie)
	owner, _ := ownerFromRequest(r)
	source := storedSource(t, s, owner)
	first, err := s.CreateJob(ctx, owner, requestFor(source), "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		j, e := s.CreateJob(ctx, owner, requestFor(source), "")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.DB.Exec(ctx, "UPDATE jobs SET status='succeeded' WHERE id=$1", j.ID); e != nil {
			t.Fatal(e)
		}
	}
	other := storedSource(t, s, "another-owner")
	if _, err = s.CreateJob(ctx, "another-owner", requestFor(other), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,expires_at) VALUES('artifact_history',$1,$2,'/private/path','clip.mp4',42,1000,3000,now()+interval '1 hour')`, owner, first.ID); err != nil {
		t.Fatal(err)
	}
	h := NewServer(Config{}, s).Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Jobs []jobSummary `json:"jobs"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Jobs) != 30 {
		t.Fatalf("invalid bounded history: %d", w.Code)
	}
	if response.Jobs[0].ID != first.ID || len(response.Jobs[0].Files) != 1 || response.Jobs[0].Files[0].ExpiresAt != nil || response.Jobs[0].Files[0].DownloadURL != "/api/v1/artifacts/artifact_history/download" {
		t.Fatal("active job or usable file omitted")
	}
	for _, j := range response.Jobs {
		if j.SourceID != source.ID {
			t.Fatal("cross-owner history leak")
		}
	}
	if _, err = s.DB.Exec(ctx, "DELETE FROM artifacts WHERE id='artifact_history'"); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs(ctx, owner)
	if err != nil || len(jobs[0].Files) != 0 {
		t.Fatal("deleted artifact remains in history")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/jobs", nil))
	if w.Code != 401 {
		t.Fatal("anonymous history allowed")
	}
}

func TestJobHistoryRetainsManifestsWithAuthoritativeArtifactFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source := storedSource(t, s, "manifest-owner")
	request := requestFor(source)
	request.OutputMode = "joined"
	request.Ranges = append(request.Ranges, Range{EndMS: 1000, Label: "Earlier"})
	if _, err := s.CreateJob(ctx, source.Owner, request, ""); err != nil {
		t.Fatal(err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	segments := []ArtifactSegment{{Index: 0, Label: "Test", SourceStartMS: 1000, SourceEndMS: 3000, OutputEndMS: 2000}, {Index: 1, Label: "Earlier", SourceEndMS: 1000, OutputStartMS: 2000, OutputEndMS: 3000}}
	tracks := []TrackSelection{{Track: Track{Index: 2, Type: "audio", Codec: "aac", Language: "rus"}, Included: true, Reason: "selected_audio"}, {Track: Track{Index: 1, Type: "audio", Codec: "aac", Language: "eng"}, Reason: "audio_not_selected"}}
	artifact := Artifact{ID: newID("art"), Filename: "joined.mp4", SizeBytes: 42, ActualEndMS: 3000, Segments: &segments, TrackManifest: &tracks}
	if err := s.AddArtifact(ctx, source.Owner, job.ID, "/private/retained-result", token, artifact); err != nil {
		t.Fatal(err)
	}
	job.Items[0].Status, job.Items[0].Artifact = "succeeded", &artifact
	job.Status = "succeeded"
	if err := s.SaveJob(ctx, job, token); err != nil {
		t.Fatal(err)
	}
	// Base artifact values and expiry come from its live owner-checked row;
	// only optional manifests are taken from the immutable published snapshot.
	if _, err := s.DB.Exec(ctx, "UPDATE artifacts SET filename='authoritative.mp4',size_bytes=84,actual_end_ms=3010,expires_at=now()+interval '1 hour' WHERE id=$1", artifact.ID); err != nil {
		t.Fatal(err)
	}
	summaries, err := s.Jobs(ctx, source.Owner)
	if err != nil || len(summaries) != 1 || len(summaries[0].Files) != 1 {
		t.Fatal("retained joined summary missing", summaries, err)
	}
	result := summaries[0].Files[0]
	if result.Filename != "authoritative.mp4" || result.SizeBytes != 84 || result.ActualEndMS != 3010 || result.ExpiresAt == nil || result.DownloadURL != "/api/v1/artifacts/"+artifact.ID+"/download" || result.Segments == nil || len(*result.Segments) != 2 || (*result.Segments)[1].Label != "Earlier" || result.TrackManifest == nil || len(*result.TrackManifest) != 2 || (*result.TrackManifest)[0].Language != "rus" {
		t.Fatal("history lost manifests or overrode authoritative artifact", result)
	}
	if other, err := s.Jobs(ctx, "other-owner"); err != nil || len(other) != 0 {
		t.Fatal("history manifests cross owner", other, err)
	}
	if _, err := s.DB.Exec(ctx, "DELETE FROM artifacts WHERE id=$1", artifact.ID); err != nil {
		t.Fatal(err)
	}
	summaries, err = s.Jobs(ctx, source.Owner)
	if err != nil || len(summaries[0].Files) != 0 {
		t.Fatal("expired manifest resurrected deleted artifact", summaries, err)
	}
}
