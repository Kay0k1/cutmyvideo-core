package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSourceAdmissionReservesOwnerBeforeReadingQuota(t *testing.T) {
	s := testStore(t)
	server := NewServer(Config{DataDir: t.TempDir(), MaxSourceBytes: 1 << 20, MaxOwnerBytes: 2 << 20, MaxStorageBytes: 10 << 20}, s)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(ctx, "LOCK TABLE sources IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	var relation uint32
	if err := lock.QueryRow(ctx, "SELECT 'sources'::regclass::oid").Scan(&relation); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.beginSource(ctx, "owner") }()
	for {
		var waiting bool
		if err := lock.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation=$1 AND mode='AccessShareLock' AND NOT granted)", relation).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	second, cancelSecond := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelSecond()
	if err := server.beginSource(second, "owner"); !errors.Is(err, errSourceBusy) {
		t.Fatalf("concurrent source request read stale quota state: %v", err)
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	server.endSource("owner")
	if len(server.preparing) != 0 || len(server.slots) != 0 {
		t.Fatal("source reservation was not released")
	}
}

func TestHTTPSourceAdmissionPreservesFailureReason(t *testing.T) {
	for _, code := range []string{"source_busy", "server_busy", "storage_limit", "internal"} {
		t.Run(code, func(t *testing.T) {
			s := testStore(t)
			c := Config{DataDir: t.TempDir(), MaxSourceBytes: 1 << 20, MaxOwnerBytes: 2 << 20, MaxStorageBytes: 10 << 20, SourceTimeout: time.Second, MutationsPerMinute: 20}
			server := NewServer(c, s)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", nil)
			request.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
			owner, _ := ownerFromRequest(request)
			status := 429
			switch code {
			case "source_busy":
				server.preparing[owner] = true
			case "server_busy":
				for range cap(server.slots) {
					server.slots <- struct{}{}
				}
			case "storage_limit":
				server.Config.MaxStorageBytes = 1
			case "internal":
				s.DB.Close()
				status = 500
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			var body struct{ Error APIError }
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != status || body.Error.Code != code {
				t.Fatalf("got %d %+v, expected %d %s", response.Code, body.Error, status, code)
			}
			if code == "internal" && (len(server.preparing) != 0 || len(server.slots) != 0) {
				t.Fatal("failed admission leaked a source reservation")
			}
		})
	}
}
