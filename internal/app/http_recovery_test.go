package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUncertainSourceErrorsKeepStatusAndWarnBeforeRetry(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	private := errors.New("private-source-commit-fixture")
	uncertain := errors.Join(ErrSourceCommitUncertain, private)
	for _, tc := range []struct {
		name   string
		source bool
		ctx    context.Context
		err    error
		status int
		code   string
	}{
		{"database", false, context.Background(), errors.Join(uncertain, errDatabaseUnavailable), 503, "database_unavailable"},
		{"request-deadline", false, expired, errors.Join(uncertain, context.DeadlineExceeded), 408, "request_timeout"},
		{"source-deadline", true, expired, errors.Join(uncertain, context.DeadlineExceeded), 504, "source_timeout"},
		{"database-before-source-deadline", true, expired, errors.Join(uncertain, errDatabaseUnavailable), 503, "database_unavailable"},
		{"unknown-internal", false, context.Background(), uncertain, 500, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if tc.source {
				writeSourcePersistenceError(response, tc.ctx, tc.err)
			} else {
				internalError(response, tc.err)
			}
			var body struct{ Error APIError }
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != tc.status || body.Error.Code != tc.code || body.Error.Message != "The source may already be saved; check your sources before retrying" {
				t.Fatal("uncertain source outcome or status changed", response.Code, body)
			}
			if strings.Contains(response.Body.String(), private.Error()) || strings.Contains(body.Error.Message, "try again") {
				t.Fatal("uncertain outcome leaked a private cause or invited a blind retry", body)
			}
		})
	}
	response := httptest.NewRecorder()
	internalError(response, errors.Join(uncertain, context.Canceled))
	if response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" {
		t.Fatal("cancelled caller received a response", response.Body.String())
	}
	response = httptest.NewRecorder()
	writeSourcePersistenceError(response, expired, context.DeadlineExceeded)
	if response.Code != 504 || !strings.Contains(response.Body.String(), "Source preparation timed out; try again") {
		t.Fatal("definite source timeout lost its existing message", response.Body.String())
	}
}

func recoveryJobResponse(t *testing.T, server *Server, cookie *http.Cookie, request ExportRequest, key string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := databaseRequest("POST", "/api/v1/jobs", string(body), cookie)
	r.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, r)
	return response
}

func TestHTTPJobReplaySurvivesChangedAdmissionLimits(t *testing.T) {
	s, server, cookie, source, _ := databaseHTTPFixture(t)
	request := requestFor(source)
	request.Ranges = append(request.Ranges, Range{StartMS: 3000, EndMS: 5000, Label: "Second"})
	accepted := recoveryJobResponse(t, server, cookie, request, "retained-recovery")
	var original Job
	if accepted.Code != http.StatusAccepted || json.Unmarshal(accepted.Body.Bytes(), &original) != nil {
		t.Fatal("initial admission failed", accepted.Code, accepted.Body.String())
	}
	// The initial submission used one owner export allowance. With valid current
	// limits, consume the other nine and distinguish recovery from new admission.
	for range 9 {
		if !server.allow("export:"+source.Owner, 10) {
			t.Fatal("could not exhaust the otherwise-valid new-work allowance")
		}
	}
	rateReplay := recoveryJobResponse(t, server, cookie, request, "retained-recovery")
	var rateRecovered Job
	if rateReplay.Code != http.StatusAccepted || json.Unmarshal(rateReplay.Body.Bytes(), &rateRecovered) != nil || rateRecovered.ID != original.ID {
		t.Fatal("exhausted new-work allowance blocked retained recovery", rateReplay.Code, rateReplay.Body.String())
	}
	rateNew := recoveryJobResponse(t, server, cookie, request, "otherwise-valid-new-work")
	if rateNew.Code != http.StatusTooManyRequests || !strings.Contains(rateNew.Body.String(), `"code":"rate_limit"`) {
		t.Fatal("new key bypassed the exhausted owner allowance", rateNew.Code, rateNew.Body.String())
	}
	c := server.Config
	c.MaxRanges, c.MaxRangeMS, c.MaxJobMS, c.MaxActiveJobs = 1, 1, 1, 1
	c.MaxStorageBytes, c.MaxOwnerBytes, c.MaxOutputBytes = 1, 1, 1
	server = NewServer(c, s)
	for range 10 {
		if !server.allow("export:"+source.Owner, 10) {
			t.Fatal("could not consume the new-work owner allowance")
		}
	}
	replayed := recoveryJobResponse(t, server, cookie, request, "retained-recovery")
	var recovered Job
	if replayed.Code != http.StatusAccepted || json.Unmarshal(replayed.Body.Bytes(), &recovered) != nil || recovered.ID != original.ID || len(recovered.Items) != 2 {
		t.Fatal("retained job could not be recovered after limit changes", replayed.Code, replayed.Body.String())
	}
	newRequest := recoveryJobResponse(t, server, cookie, request, "new-work")
	if newRequest.Code != http.StatusBadRequest || !strings.Contains(newRequest.Body.String(), "invalid_export") {
		t.Fatal("new work bypassed current admission validation", newRequest.Code, newRequest.Body.String())
	}
	var count int
	if err := s.DB.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE owner=$1 AND idempotency_key=$2", source.Owner, "retained-recovery").Scan(&count); err != nil || count != 1 {
		t.Fatal("recovery created duplicate work", count, err)
	}
}

func TestHTTPJobReplayKeepsKeyOnlyContractAndOwnerPrivacy(t *testing.T) {
	s, server, cookie, source, _ := databaseHTTPFixture(t)
	original, err := s.CreateJob(context.Background(), source.Owner, requestFor(source), "private-recovery")
	if err != nil {
		t.Fatal(err)
	}
	changed := ExportRequest{SourceID: "absent-or-foreign-source", Format: "invalid"}
	response := recoveryJobResponse(t, server, cookie, changed, "private-recovery")
	var recovered Job
	if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &recovered) != nil || recovered.ID != original.ID {
		t.Fatal("reused key stopped returning its original job", response.Code, response.Body.String())
	}
	for _, key := range []string{"new-work", ""} {
		response = recoveryJobResponse(t, server, cookie, changed, key)
		if response.Code != 404 || strings.Contains(response.Body.String(), original.ID) {
			t.Fatal("new key bypassed source ownership", response.Code, response.Body.String())
		}
	}
	if job, err := s.JobByIdempotencyKey(context.Background(), "another-owner", "private-recovery"); !errors.Is(err, ErrNotFound) || job.ID != "" {
		t.Fatal("idempotency lookup disclosed another owner's job", job.ID, err)
	}
	identity := make([]byte, 32)
	identity[0] = 1
	foreign := &http.Cookie{Name: cookie.Name, Value: base64.RawURLEncoding.EncodeToString(identity)}
	response = recoveryJobResponse(t, server, foreign, requestFor(source), "private-recovery")
	if response.Code != 404 || strings.Contains(response.Body.String(), original.ID) {
		t.Fatal("HTTP replay disclosed another owner's job", response.Code, response.Body.String())
	}
	response = recoveryJobResponse(t, server, cookie, requestFor(source), strings.Repeat("k", 129))
	if response.Code != 400 {
		t.Fatal("replay lookup bypassed the key bound", response.Code, response.Body.String())
	}
	r := databaseRequest("POST", "/api/v1/jobs", `{"unexpected":true}`, cookie)
	r.Header.Set("Idempotency-Key", "private-recovery")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, r)
	if response.Code != 400 {
		t.Fatal("replay lookup bypassed JSON validation", response.Code, response.Body.String())
	}
}

func TestHTTPConcurrentFirstSubmissionsKeepOneJob(t *testing.T) {
	s, server, cookie, source, _ := databaseHTTPFixture(t)
	body, err := json.Marshal(requestFor(source))
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	var ready sync.WaitGroup
	ready.Add(8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			ready.Done()
			<-start
			r := databaseRequest("POST", "/api/v1/jobs", string(body), cookie)
			r.Header.Set("Idempotency-Key", "concurrent-recovery")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, r)
			responses <- response
		}()
	}
	ready.Wait()
	close(start)
	var id string
	for range 8 {
		var response *httptest.ResponseRecorder
		select {
		case response = <-responses:
		case <-time.After(20 * time.Second):
			t.Fatal("concurrent submission did not finish within the test bound")
		}
		var job Job
		if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &job) != nil {
			t.Fatal("concurrent submission failed", response.Code, response.Body.String())
		}
		if id != "" && id != job.ID {
			t.Fatal("concurrent submission created multiple jobs", id, job.ID)
		}
		id = job.ID
	}
	var count int
	if err := s.DB.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE owner=$1 AND idempotency_key=$2", source.Owner, "concurrent-recovery").Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent submissions were not deduplicated", count, err)
	}
}
