package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourceMediaReaderSaturationHasRetryableDiagnostic(t *testing.T) {
	s, c, source := sourceCleanupFixture(t)
	ctx := context.Background()
	// A large stale history must not hide the active cap or grow admission work.
	sourcePinExec(t, s.DB, `INSERT INTO source_media_readers(id,source_id,expires_at)
 SELECT 'expired-'||i,$1,now()-interval '1 day' FROM generate_series(1,2000) i`, source.ID)
	sourcePinExec(t, s.DB, `INSERT INTO source_media_readers(id,source_id,expires_at)
 SELECT 'active-'||i,$1,now()+interval '1 hour' FROM generate_series(1,128) i`, source.ID)
	if _, err := s.acquireSourceMediaReader(ctx, source, "denied"); !errors.Is(err, errSourceServerBusy) {
		t.Fatal("reader cap ignored", err)
	}
	server := NewServer(c, s)
	for _, handler := range []ownerHandler{server.sourceMedia, server.sourceThumbnail, server.sourcePreview, server.sourceWaveform} {
		request := httptest.NewRequest(http.MethodGet, "/source?start_ms=0", nil)
		request.SetPathValue("id", source.ID)
		response := httptest.NewRecorder()
		handler(response, request, source.Owner)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"server_busy"`) {
			t.Fatal("saturation was not retryable", response.Code, response.Body.String())
		}
	}
	sourcePinExec(t, s.DB, `DELETE FROM source_media_readers WHERE id='active-1'`)
	if _, err := s.acquireSourceMediaReader(ctx, source, "admitted"); err != nil {
		t.Fatal("stale rows incorrectly blocked available reader", err)
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM source_media_readers WHERE expires_at>now()`).Scan(&count); err != nil || count != 128 {
		t.Fatal("active cap/count disagrees", count, err)
	}
}

func TestExpiredSourceMediaReadersPruneInBoundedUnlockedBatches(t *testing.T) {
	s, _, source := sourceCleanupFixture(t)
	ctx := context.Background()
	sourcePinExec(t, s.DB, `INSERT INTO source_media_readers(id,source_id,expires_at)
 SELECT 'expired-'||lpad(i::text,4,'0'),$1,now()-interval '1 day' FROM generate_series(1,450) i`, source.ID)
	sourcePinExec(t, s.DB, `INSERT INTO source_media_readers(id,source_id,expires_at)VALUES('active',$1,now()+interval '1 hour')`, source.ID)
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	if _, err = lock.Exec(ctx, `SELECT id FROM source_media_readers WHERE id='expired-0001' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err = s.cleanupSourceMediaReaders(deadline); err != nil {
		t.Fatal("locked stale lease blocked bounded cleanup", err)
	}
	var stale, active int
	count := func() {
		t.Helper()
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FILTER(WHERE expires_at<=now()),count(*) FILTER(WHERE expires_at>now()) FROM source_media_readers`).Scan(&stale, &active); err != nil {
			t.Fatal(err)
		}
	}
	// Release the separate fixture transaction before observing via the two-slot
	// pool; its locked expired reader must have been skipped and preserved.
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	count()
	if stale != 250 || active != 1 {
		t.Fatal("batch exceeded200 or removed active reader", stale, active)
	}
	var protected bool
	if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_media_readers WHERE id='expired-0001')`).Scan(&protected); err != nil || !protected {
		t.Fatal("locked lease was removed", protected, err)
	}
	if err = s.cleanupSourceMediaReaders(ctx); err != nil {
		t.Fatal(err)
	}
	count()
	if stale != 50 || active != 1 {
		t.Fatal("second bounded batch wrong", stale, active)
	}
	if err = s.cleanupSourceMediaReaders(ctx); err != nil {
		t.Fatal(err)
	}
	count()
	if stale != 0 || active != 1 {
		t.Fatal("crashed reader history did not converge", stale, active)
	}
}

func TestUncertainSourceMediaReaderReleaseWaitsForItsAdmission(t *testing.T) {
	s, _, source := sourceCleanupFixture(t)
	ctx := context.Background()
	// The admission transaction still holds the storage fence and has not yet
	// committed its lease when the caller gives up on the COMMIT.
	admission, err := s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(admission)
	if _, err = admission.Exec(ctx, `INSERT INTO source_media_readers(id,source_id,expires_at) VALUES('uncertain',$1,clock_timestamp()+interval '60 seconds')`, source.ID); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		released <- s.releaseUncertainSourceMediaReader(deadline, source.ID, "uncertain")
	}()
	select {
	case err := <-released:
		t.Fatal("release did not wait for the admission transaction", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err = admission.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-released; err != nil {
		t.Fatal(err)
	}
	var leaked bool
	if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_media_readers WHERE id='uncertain')`).Scan(&leaked); err != nil || leaked {
		t.Fatal("uncertain admission left its reader lease", leaked, err)
	}
	if _, err = s.DeleteSource(ctx, source.ID, source.Owner); err != nil {
		t.Fatal("released lease still blocks source deletion", err)
	}
}
