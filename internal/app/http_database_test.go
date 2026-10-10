package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/puddle/v2"
)

func databaseHTTPFixture(t *testing.T) (*Store, *Server, *http.Cookie, Source, Job) {
	t.Helper()
	s := testStore(t)
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(cookie)
	owner, _ := ownerFromRequest(r)
	source := storedSource(t, s, owner)
	j, err := s.CreateJob(context.Background(), owner, requestFor(source), "")
	if err != nil {
		t.Fatal(err)
	}
	c := Config{DataDir: t.TempDir(), SourceTimeout: time.Minute, UploadTimeout: time.Minute, MutationsPerMinute: 100,
		MaxRanges: 32, MaxRangeMS: 60000, MaxJobMS: 60000, MaxActiveJobs: 32, MaxSourceBytes: 1 << 20, MaxOutputBytes: 1 << 20}
	return s, NewServer(c, s), cookie, source, j
}

func databaseRequest(method, path, body string, cookie *http.Cookie) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "database-recovery")
	return r
}

func assertDatabaseUnavailable(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body struct{ Error APIError }
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || body.Error.Code != "database_unavailable" || body.Error.Message != "Database is temporarily unavailable; try again" {
		t.Fatalf("got %d %+v", response.Code, body.Error)
	}
}

func TestAPIDatabasePoolWaitsAreBoundedAndRecover(t *testing.T) {
	s, server, cookie, source, j := databaseHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var held []*pgxpool.Conn
	for range s.DB.Config().MaxConns {
		conn, err := s.DB.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	release := func() {
		for _, conn := range held {
			conn.Release()
		}
		held = nil
	}
	defer release()
	export, _ := json.Marshal(requestFor(source))
	type result struct {
		path     string
		response *httptest.ResponseRecorder
		elapsed  time.Duration
	}
	cases := []struct{ method, path, body string }{
		{"GET", "/api/v1/sources", ""},
		{"GET", "/api/v1/sources/" + source.ID, ""},
		{"GET", "/api/v1/sources/" + source.ID + "/media", ""},
		{"GET", "/api/v1/sources/" + source.ID + "/thumbnail", ""},
		{"GET", "/api/v1/sources/" + source.ID + "/preview?start_ms=0", ""},
		{"GET", "/api/v1/jobs", ""},
		{"GET", "/api/v1/jobs/" + j.ID, ""},
		{"GET", "/api/v1/artifacts/absent/download", ""},
		{"POST", "/api/v1/jobs", string(export)},
		{"POST", "/api/v1/jobs/" + j.ID + "/cancel", ""},
		{"DELETE", "/api/v1/sources/" + source.ID, ""},
		{"POST", "/api/v1/uploads", ""},
		{"POST", "/api/v1/sources", `{"url":"https://youtu.be/abcdefghijk"}`},
	}
	results := make(chan result, len(cases))
	for _, tc := range cases {
		go func() {
			started := time.Now()
			response := httptest.NewRecorder()
			requestCookie := cookie
			if tc.method == "POST" && (tc.path == "/api/v1/uploads" || tc.path == "/api/v1/sources") {
				identity := make([]byte, 32)
				identity[0] = 1
				if tc.path == "/api/v1/sources" {
					identity[0] = 2
				}
				requestCookie = &http.Cookie{Name: cookie.Name, Value: base64.RawURLEncoding.EncodeToString(identity)}
			}
			server.Handler().ServeHTTP(response, databaseRequest(tc.method, tc.path, tc.body, requestCookie))
			results <- result{tc.method + " " + tc.path, response, time.Since(started)}
		}()
	}
	for range cases {
		select {
		case got := <-results:
			t.Logf("%s elapsed=%s status=%d", got.path, got.elapsed, got.response.Code)
			if got.response.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s returned %d: %s", got.path, got.response.Code, got.response.Body.String())
			}
			assertDatabaseUnavailable(t, got.response)
			// Failed source preparations also perform a separately bounded cleanup.
			if got.elapsed > 2*apiDatabaseTimeout+2*time.Second {
				t.Fatalf("%s exceeded database/cleanup bounds: %s", got.path, got.elapsed)
			}
		case <-ctx.Done():
			t.Fatal("pool exhaustion left an API request blocked")
		}
	}
	release()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, databaseRequest("GET", "/api/v1/sources/"+source.ID, "", cookie))
	if response.Code != http.StatusOK {
		t.Fatalf("API did not recover after releasing pool: %d", response.Code)
	}
	server.mu.Lock()
	leaked := len(server.preparing) != 0 || len(server.slots) != 0
	server.mu.Unlock()
	if leaked {
		t.Fatal("failed database admission retained a local source slot")
	}
}

func TestAPIDatabaseTableLockTimeoutAndRecovery(t *testing.T) {
	s, server, cookie, source, _ := databaseHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	if _, err = lock.Exec(ctx, "LOCK TABLE sources IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, databaseRequest("GET", "/api/v1/sources/"+source.ID, "", cookie))
	t.Logf("table-lock elapsed=%s status=%d", time.Since(started), response.Code)
	assertDatabaseUnavailable(t, response)
	if time.Since(started) > apiDatabaseTimeout+2*time.Second {
		t.Fatal("table lock exceeded API database deadline")
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, databaseRequest("GET", "/api/v1/sources/"+source.ID, "", cookie))
	if response.Code != http.StatusOK {
		t.Fatal("API did not recover after table lock release", response.Code)
	}
}

func TestAPIDatabaseAdmissionTimeoutReleasesTransactionLocks(t *testing.T) {
	s, server, cookie, source, _ := databaseHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	// Admission acquires the global storage lock before this owner lock. Its
	// timeout must release the already-acquired lock as well as its connection.
	if _, err = lock.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", source.Owner); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(requestFor(source))
	started := time.Now()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, databaseRequest("POST", "/api/v1/jobs", string(body), cookie))
		done <- response
	}()
	observed := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		var acquired bool
		err = lock.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", storageLock).Scan(&acquired)
		if err != nil {
			t.Fatal(err)
		}
		if !acquired {
			observed = true
			break
		}
		if _, err = lock.Exec(ctx, "SELECT pg_advisory_unlock($1)", storageLock); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("admission never acquired its transaction storage lock")
	}
	select {
	case response := <-done:
		t.Logf("advisory-lock admission elapsed=%s status=%d", time.Since(started), response.Code)
		assertDatabaseUnavailable(t, response)
	case <-ctx.Done():
		t.Fatal("advisory lock wait did not finish")
	}
	var acquired bool
	// pgx cancels and closes interrupted connections asynchronously. Give that
	// cleanup a bounded grace period, then prove the server-side lock disappeared.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if err = lock.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", storageLock).Scan(&acquired); err != nil {
			t.Fatal(err)
		}
		if acquired {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !acquired {
		t.Fatal("timed-out transaction retained the global storage lock")
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var first Job
	for i := range 2 {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, databaseRequest("POST", "/api/v1/jobs", string(body), cookie))
		if response.Code != http.StatusAccepted {
			t.Fatal("idempotent admission did not recover", response.Code, response.Body.String())
		}
		var job Job
		if err = json.Unmarshal(response.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = job
		} else if job.ID != first.ID {
			t.Fatal("idempotency replay created a duplicate job")
		}
	}
}

func TestAPIDatabaseHonorsEarlierCallerCancellation(t *testing.T) {
	s, server, cookie, source, _ := databaseHTTPFixture(t)
	var held []*pgxpool.Conn
	for range s.DB.Config().MaxConns {
		conn, err := s.DB.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		defer conn.Release()
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
		} else {
			cancel()
		}
		response := httptest.NewRecorder()
		started := time.Now()
		server.Handler().ServeHTTP(response, databaseRequest("GET", "/api/v1/sources/"+source.ID, "", cookie).WithContext(ctx))
		cancel()
		if time.Since(started) > time.Second || strings.Contains(response.Body.String(), "database_unavailable") {
			t.Fatal("caller cancellation became a database outage or waited for the database budget")
		}
		if deadline && response.Code != http.StatusRequestTimeout {
			t.Fatal("earlier request deadline was not preserved", response.Code)
		}
	}
}

func TestAPIDatabaseErrorPreservesPublicationUncertaintyAndPrivacy(t *testing.T) {
	parent := context.Background()
	ctx, cancel := context.WithCancelCause(parent)
	cancel(errAPIDatabaseTimeout)
	private := errors.New("private-connection-fixture")
	err := apiDatabaseError(parent, ctx, errors.Join(ErrSourceCommitUncertain, private))
	if !errors.Is(err, ErrSourceCommitUncertain) || !errors.Is(err, errDatabaseUnavailable) || !errors.Is(err, private) {
		t.Fatal("database classification discarded the uncertain publication cause")
	}
	response := httptest.NewRecorder()
	internalError(response, err)
	assertDatabaseUnavailable(t, response)
	if strings.Contains(response.Body.String(), private.Error()) {
		t.Fatal("database diagnostic disclosed its private cause")
	}
	for _, cause := range []error{io.EOF, puddle.ErrClosedPool, &pgconn.PgError{Code: "57P01"}} {
		if !errors.Is(apiDatabaseError(parent, parent, cause), errDatabaseUnavailable) {
			t.Fatal("connection outage retained internal error", cause)
		}
	}
	constraint := &pgconn.PgError{Code: "23505"}
	if errors.Is(apiDatabaseError(parent, parent, constraint), errDatabaseUnavailable) {
		t.Fatal("constraint failure incorrectly reported a database outage")
	}
}

func TestAPIDatabaseCallerCancellationWinsDriverFailure(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		for _, cause := range []struct {
			name string
			err  error
		}{
			{"closed-pool", puddle.ErrClosedPool},
			{"lost-connection", io.EOF},
			{"sql-failure", &pgconn.PgError{Code: "23505"}},
		} {
			name := "cancel/" + cause.name
			if deadline {
				name = "deadline/" + cause.name
			}
			t.Run(name, func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					parent, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
				}
				defer cancel()
				// The request is live when the stage starts. Model an independent
				// driver failure returned as caller cancellation becomes visible.
				err := apiDatabaseExec(parent, func(ctx context.Context) error {
					if deadline {
						<-ctx.Done()
					} else {
						cancel()
					}
					return errors.Join(ErrSourceCommitUncertain, cause.err)
				})
				if !errors.Is(err, parent.Err()) || !errors.Is(err, cause.err) || !errors.Is(err, ErrSourceCommitUncertain) {
					t.Fatal("caller cancellation or uncertain publication/driver cause was lost", err)
				}
				if errors.Is(err, errDatabaseUnavailable) {
					t.Fatal("caller cancellation was classified as a database outage")
				}
				response := httptest.NewRecorder()
				internalError(response, err)
				if deadline {
					if response.Code != http.StatusRequestTimeout || !strings.Contains(response.Body.String(), `"code":"request_timeout"`) {
						t.Fatal("caller deadline did not produce the request timeout contract", response.Code, response.Body.String())
					}
					sourceResponse := httptest.NewRecorder()
					writeSourcePersistenceError(sourceResponse, parent, err)
					if sourceResponse.Code != http.StatusGatewayTimeout || !strings.Contains(sourceResponse.Body.String(), `"code":"source_timeout"`) {
						t.Fatal("source deadline lost its own timeout contract", sourceResponse.Code, sourceResponse.Body.String())
					}
				} else if response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" {
					t.Fatal("cancelled caller received an internal/database error response", response.Body.String())
				}
			})
		}
	}
}

func TestAPIDatabaseSuccessfulOperationSurvivesLateCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	value, err := apiDatabase(parent, func(context.Context) (int, error) {
		// Acknowledged success must stay successful, even if the caller cancels
		// immediately afterward; this must not imply that a mutation failed.
		cancel()
		return 42, nil
	})
	if err != nil || value != 42 {
		t.Fatal("known success was turned into a cancellation failure", value, err)
	}
}

func TestOpenStoreCancelledMigrationReleasesPoolBeforeClose(t *testing.T) {
	s := testStore(t)
	lockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := s.DB.Begin(lockCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	if _, err = lock.Exec(lockCtx, "SELECT pg_advisory_xact_lock(hashtext('cutmy:migrations'))"); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		opened, err := OpenStore(ctx, s.DB.Config().ConnString())
		if opened != nil {
			opened.DB.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("migration cancellation cause lost: %T", err)
		}
		t.Logf("cancelled initialization elapsed=%s", time.Since(started))
	case <-time.After(2 * time.Second):
		t.Fatal("OpenStore waited for its borrowed transaction while closing the pool")
	}
	if err = lock.Rollback(lockCtx); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenStore(lockCtx, s.DB.Config().ConnString())
	if err != nil {
		t.Fatalf("initialization did not recover after migration lock release: %T", err)
	}
	opened.DB.Close()
}
