package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func smallAdmissionSource(t *testing.T, store *Store, owner string) Source {
	t.Helper()
	source := storedSource(t, store, owner)
	source.Width, source.Height = 160, 90
	if _, err := store.DB.Exec(context.Background(), "UPDATE sources SET width=$2,height=$3 WHERE id=$1", source.ID, source.Width, source.Height); err != nil {
		t.Fatal(err)
	}
	return source
}

func assertAdmissionReleased(t *testing.T, store *Store, c Config, source Source) {
	t.Helper()
	var active, pins int
	var reserved int64
	if err := store.DB.QueryRow(context.Background(), "SELECT count(*),COALESCE(sum(size_bytes),0) FROM storage_reservations WHERE kind='preview'").Scan(&active, &reserved); err != nil {
		t.Fatal(err)
	}
	if active != 0 || reserved != 0 {
		t.Fatalf("failed admission retained %d processing leases and %d bytes", active, reserved)
	}
	if err := store.DB.QueryRow(context.Background(), "SELECT retention_preview_pins FROM sources WHERE id=$1", source.ID).Scan(&pins); err != nil || pins != 0 {
		t.Fatalf("failed admission retained source pins: %d %v", pins, err)
	}
	other := smallAdmissionSource(t, store, "another-owner")
	if _, err := store.acquirePreview(context.Background(), c, other, newID("preview"), "unrelated-background", true); err != nil {
		t.Fatalf("unrelated owner remains blocked after failed admission: %v", err)
	}
}

func TestWindowFailedAdmissionReleasesPossiblyCommittedLease(t *testing.T) {
	for _, storyboard := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			t.Run(fmt.Sprintf("storyboard=%t/canceled=%t", storyboard, canceled), func(t *testing.T) {
				store := testStore(t)
				c := ledgerConfig(t)
				c.MaxStorageBytes = 1 << 30
				store.ConfigureStorage(c)
				source := smallAdmissionSource(t, store, "owner")
				app := NewServer(c, store)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				request := httptest.NewRequest(http.MethodGet, "/preview?start_ms=0", nil).WithContext(ctx)
				request.SetPathValue("id", source.ID)
				acquire := func(ctx context.Context, c Config, source Source, id, key string, background, _ bool) (*os.File, error) {
					file, err := store.acquirePreview(ctx, c, source, id, key, background)
					if err != nil || file != nil {
						t.Fatalf("fixture admission failed: %v", err)
					}
					// SQL has committed, but the caller has no acknowledgement.
					if canceled {
						cancel()
						return nil, errors.Join(errPreviewAdmissionUncertain, context.Canceled)
					}
					return nil, errors.Join(errPreviewAdmissionUncertain, io.ErrUnexpectedEOF)
				}
				app.sourceWindowWithAdmission(httptest.NewRecorder(), request, source.Owner, storyboard, store.publishPreview, acquire)
				assertAdmissionReleased(t, store, c, source)
			})
		}
	}
}

func TestInspectionFailedAdmissionReleasesPossiblyCommittedLease(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			store := testStore(t)
			c := ledgerConfig(t)
			c.MaxStorageBytes = 1 << 30
			store.ConfigureStorage(c)
			source := smallAdmissionSource(t, store, "owner")
			// Persisted dimensions may describe a historical cover image.
			source.Width, source.Height = 0, 0
			app := NewServer(c, store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := error(io.ErrUnexpectedEOF)
			if canceled {
				failure = context.Canceled
			}
			reserve := func(ctx context.Context, source Source, id string, timeout time.Duration) error {
				if err := store.reserveSourceInspection(ctx, source, id, timeout); err != nil {
					t.Fatal(err)
				}
				if canceled {
					cancel()
				}
				return errors.Join(errPreviewAdmissionUncertain, failure)
			}
			release, err := app.pinSourceInspectionWithReserve(ctx, source, time.Minute, reserve)
			if release != nil || !errors.Is(err, failure) {
				t.Fatalf("failed admission changed its original error: %v", err)
			}
			assertAdmissionReleased(t, store, c, source)
		})
	}
}

func TestAdmissionCleanupWaitsForLateCommit(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		t.Run(fmt.Sprint(workspace), func(t *testing.T) {
			store := testStore(t)
			c := ledgerConfig(t)
			c.MaxStorageBytes = 1 << 30
			store.ConfigureStorage(c)
			source := smallAdmissionSource(t, store, "owner")
			observer, err := pgx.Connect(context.Background(), os.Getenv("TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close(context.Background())
			admission, err := store.storageTx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackStorage(admission)
			id := newID("preview")
			charge := int64(0)
			if workspace {
				charge = previewReservationBytes
			}
			if _, err := admission.Exec(context.Background(), `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id,token) VALUES($1,$2,'preview',$3,now()+interval '1 minute',$4,'late-admission')`, id, source.Owner, charge, source.ID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				result <- store.releasePreviewAdmission(ctx, c, source, id, workspace)
			}()
			// Observe the actual advisory-lock wait, rather than guessing that a
			// sleeping cleanup goroutine has reached its database operation.
			deadline := time.Now().Add(2 * time.Second)
			for {
				select {
				case err := <-result:
					t.Fatalf("cleanup missed uncommitted admission instead of waiting: %v", err)
				default:
				}
				var waiting bool
				if err := observer.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))", admission.Conn().PgConn().PID()).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("cleanup never waited on admission fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := admission.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			assertAdmissionReleased(t, store, c, source)
		})
	}
}

func TestAdmissionCleanupPreservesOtherOwnersIDsAndCompletedCache(t *testing.T) {
	for _, scenario := range []string{"other-owner", "other-id", "completed-cache"} {
		t.Run(scenario, func(t *testing.T) {
			store := testStore(t)
			c := ledgerConfig(t)
			c.MaxStorageBytes = 1 << 30
			store.ConfigureStorage(c)
			source := smallAdmissionSource(t, store, "owner")
			id := newID("preview")
			if _, err := store.acquirePreview(context.Background(), c, source, id, "protected", false); err != nil {
				t.Fatal(err)
			}
			file := cachedPreviewFixture(t, c, id, 1024)
			file.Close()
			cleanupSource, cleanupID := source, id
			if scenario == "other-owner" {
				cleanupSource.Owner = "another-owner"
			}
			if scenario == "other-id" {
				cleanupID = newID("preview")
			}
			if scenario == "completed-cache" {
				if _, err := store.DB.Exec(context.Background(), "UPDATE storage_reservations SET kind='preview_cache',size_bytes=1024 WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.releasePreviewAdmission(context.Background(), c, cleanupSource, cleanupID, true); err != nil {
				t.Fatal(err)
			}
			var exists bool
			if err := store.DB.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE id=$1)", id).Scan(&exists); err != nil || !exists {
				t.Fatalf("cleanup deleted protected lease: %v", err)
			}
			if _, err := os.Stat(filepath.Join(c.DataDir, "previews", id, "preview.mp4")); err != nil {
				t.Fatalf("cleanup removed protected media: %v", err)
			}
		})
	}
}

func TestAdmissionCleanupFenceTimeoutKeepsChargeAndPins(t *testing.T) {
	store := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	store.ConfigureStorage(c)
	source := smallAdmissionSource(t, store, "owner")
	id := newID("preview")
	if _, err := store.acquirePreview(context.Background(), c, source, id, "protected", false); err != nil {
		t.Fatal(err)
	}
	blocker, err := store.storageTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(blocker)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = store.releasePreviewAdmission(ctx, c, source, id, true)
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("cleanup did not honor its bounded context: %v", err)
	}
	if err := blocker.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var charge int64
	var pins int
	if err := store.DB.QueryRow(context.Background(), "SELECT size_bytes FROM storage_reservations WHERE id=$1", id).Scan(&charge); err != nil || charge != previewReservationBytes {
		t.Fatalf("failed cleanup released charge: %d %v", charge, err)
	}
	if err := store.DB.QueryRow(context.Background(), "SELECT retention_preview_pins FROM sources WHERE id=$1", source.ID).Scan(&pins); err != nil || pins != 1 {
		t.Fatalf("failed cleanup released pin: %d %v", pins, err)
	}
}

type admissionCommitFailure struct {
	pgx.Tx
	failure error
}

func (tx admissionCommitFailure) Commit(context.Context) error { return tx.failure }

type retrySafeAdmissionFailure struct{}

func (retrySafeAdmissionFailure) Error() string     { return "COMMIT was not sent" }
func (retrySafeAdmissionFailure) SafeToRetry() bool { return true }

func TestAdmissionCommitClassifiesOnlyUnknownOutcomes(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded, pgx.ErrTxCommitRollback, &pgconn.PgError{Code: "23514"}, retrySafeAdmissionFailure{}} {
		err := commitPreviewAdmission(context.Background(), admissionCommitFailure{failure: cause})
		if !errors.Is(err, cause) {
			t.Fatal("admission classification lost original cause")
		}
		uncertain := cause == io.ErrUnexpectedEOF || cause == context.Canceled || cause == context.DeadlineExceeded
		if errors.Is(err, errPreviewAdmissionUncertain) != uncertain {
			t.Fatalf("unexpected admission commit classification for %T", cause)
		}
	}
}

type admissionCanceledAcknowledgement struct {
	pgx.Tx
	cancel context.CancelFunc
}

func (tx admissionCanceledAcknowledgement) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	tx.cancel()
	return context.Canceled
}

func TestAdmissionCommitMarksActualCommittedLostAcknowledgement(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			store := testStore(t)
			c := ledgerConfig(t)
			store.ConfigureStorage(c)
			source := smallAdmissionSource(t, store, "owner")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tx, err := store.storageTx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackStorage(tx)
			id := newID("inspection")
			if _, err := tx.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id,token) VALUES($1,$2,'preview',0,now()+interval '1 minute',$3,'inspection-full:committed')`, id, source.Owner, source.ID); err != nil {
				t.Fatal(err)
			}
			wrapped := pgx.Tx(publicationLostAcknowledgement{Tx: tx})
			cause := error(io.ErrUnexpectedEOF)
			if canceled {
				wrapped = admissionCanceledAcknowledgement{Tx: tx, cancel: cancel}
				cause = context.Canceled
			}
			if err := commitPreviewAdmission(ctx, wrapped); !errors.Is(err, errPreviewAdmissionUncertain) || !errors.Is(err, cause) {
				t.Fatalf("committed admission lost uncertainty/cause: %v", err)
			}
			var exists bool
			if err := store.DB.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM storage_reservations WHERE id=$1)", id).Scan(&exists); err != nil || !exists {
				t.Fatalf("fault did not actually commit admission: %v", err)
			}
		})
	}
}

func TestInspectionFailedCleanupPreservesOriginalErrorAndMaintenanceRecovery(t *testing.T) {
	store := testStore(t)
	c := ledgerConfig(t)
	c.MaxStorageBytes = 1 << 30
	store.ConfigureStorage(c)
	source := smallAdmissionSource(t, store, "owner")
	app := NewServer(c, store)
	var schema string
	if err := store.DB.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	reserve := func(ctx context.Context, source Source, id string, timeout time.Duration) error {
		if err := store.reserveSourceInspection(ctx, source, id, timeout); err != nil {
			t.Fatal(err)
		}
		store.DB.Close() // The fresh cleanup attempt cannot acquire a connection.
		return errors.Join(errPreviewAdmissionUncertain, io.ErrUnexpectedEOF)
	}
	if release, err := app.pinSourceInspectionWithReserve(context.Background(), source, time.Minute, reserve); release != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("cleanup failure replaced primary error: %v", err)
	}
	config, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store.DB = pool
	var count, pins int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM storage_reservations WHERE kind='preview'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed cleanup lost conservative lease: %d %v", count, err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT retention_preview_pins FROM sources WHERE id=$1", source.ID).Scan(&pins); err != nil || pins != 1 {
		t.Fatalf("failed cleanup lost conservative source pin: %d %v", pins, err)
	}
	if _, err := pool.Exec(context.Background(), "UPDATE storage_reservations SET expires_at=now()-interval '1 second' WHERE kind='preview'"); err != nil {
		t.Fatal(err)
	}
	if err := store.cleanupPreviews(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertAdmissionReleased(t, store, c, source)
}
