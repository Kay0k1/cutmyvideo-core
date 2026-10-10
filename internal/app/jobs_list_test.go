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
