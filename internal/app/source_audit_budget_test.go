package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// This context expires synchronously at a chosen unit boundary. These loop
// controls do not simulate database COMMIT; the PostgreSQL tests below do.
type sourceAuditTestContext struct {
	context.Context
	done    chan struct{}
	expired bool
}

func newSourceAuditTestContext() *sourceAuditTestContext {
	return &sourceAuditTestContext{Context: context.Background(), done: make(chan struct{})}
}
func (c *sourceAuditTestContext) Done() <-chan struct{} {
	return c.done
}
func (c *sourceAuditTestContext) Err() error {
	if c.expired {
		return context.DeadlineExceeded
	}
	return nil
}
func (c *sourceAuditTestContext) expire() {
	c.expired = true
	close(c.done)
}

func TestSourcePinAuditOwnedBudgetKeepsContinuationAndAcknowledgedRepairs(t *testing.T) {
	for _, scenario := range []string{"first-unit", "deadline-wrappers", "after-acknowledged-unit", "acknowledged-eof", "unit-cap"} {
		t.Run(scenario, func(t *testing.T) {
			audit := newSourceAuditTestContext()
			calls := 0
			more, eligible, err := auditSourcePinUnits(context.Background(), audit, func(context.Context) (bool, bool, error) {
				calls++
				switch scenario {
				case "first-unit":
					audit.expire()
					// Neither an EOF nor a repair hint is acknowledged with this error.
					return true, true, fmt.Errorf("timeout: %w", context.DeadlineExceeded)
				case "deadline-wrappers":
					audit.expire()
					return true, true, errors.Join(context.DeadlineExceeded, fmt.Errorf("timeout: %w", context.DeadlineExceeded))
				case "after-acknowledged-unit":
					if calls == 1 {
						audit.expire()
						return false, true, nil
					}
					return true, false, context.DeadlineExceeded
				case "acknowledged-eof":
					return true, false, nil
				default:
					return false, false, nil
				}
			})
			wantCalls, wantMore, wantEligible := 1, true, false
			switch scenario {
			case "after-acknowledged-unit":
				wantCalls, wantEligible = 2, true
			case "acknowledged-eof":
				wantMore = false
			case "unit-cap":
				wantCalls = sourcePinAuditSources
			}
			if err != nil || calls != wantCalls || more != wantMore || eligible != wantEligible {
				t.Fatal("budget lost continuation, accepted a failed hint, or exceeded unit cap", calls, more, eligible, err)
			}
		})
	}
}

type sourceAuditForeignDeadline struct{}

func (sourceAuditForeignDeadline) Error() string {
	return "foreign error claiming a deadline match"
}
func (sourceAuditForeignDeadline) Is(target error) bool {
	return target == context.DeadlineExceeded
}

func TestSourcePinAuditOwnedBudgetPreservesGenuineFailures(t *testing.T) {
	lock := &pgconn.PgError{Code: "55P03", Message: "lock timeout"}
	constraint := &pgconn.PgError{Code: "23514", Message: "constraint failure"}
	connection := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	for _, failure := range []error{lock, constraint, connection, io.ErrUnexpectedEOF, context.Canceled,
		errors.Join(context.DeadlineExceeded, lock), errors.Join(connection, context.DeadlineExceeded),
		fmt.Errorf("wrapped connection: %w", io.ErrUnexpectedEOF), sourceAuditForeignDeadline{}} {
		audit := newSourceAuditTestContext()
		audit.expire()
		more, eligible, err := auditSourcePinUnits(context.Background(), audit, func(context.Context) (bool, bool, error) {
			return true, true, failure
		})
		if !more || eligible || !errors.Is(err, failure) {
			t.Fatal("own expiry hid a genuine error or accepted an unacknowledged repair", failure, more, eligible, err)
		}
	}
	// A deadline from somewhere else is not attributed to a still-live audit.
	more, eligible, err := auditSourcePinUnits(context.Background(), context.Background(), func(context.Context) (bool, bool, error) {
		return true, true, context.DeadlineExceeded
	})
	if !more || eligible || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("foreign deadline was silently deferred", more, eligible, err)
	}
}

func TestSourcePinAuditParentCancellationHasPrecedence(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		audit := newSourceAuditTestContext()
		if alreadyCanceled {
			cancel()
		}
		calls := 0
		more, eligible, err := auditSourcePinUnits(ctx, audit, func(context.Context) (bool, bool, error) {
			calls++
			audit.expire()
			cancel()
			return true, true, context.DeadlineExceeded
		})
		cancel()
		wantCalls := 1
		if alreadyCanceled {
			wantCalls = 0
		}
		if calls != wantCalls || !more || eligible || !errors.Is(err, context.Canceled) {
			t.Fatal("parent cancellation became owned-budget success", calls, more, eligible, err)
		}
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	calls := 0
	more, eligible, err := auditSourcePinUnits(ctx, context.Background(), func(context.Context) (bool, bool, error) {
		calls++
		return true, true, nil
	})
	if calls != 0 || !more || eligible || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired caller was treated as audit EOF", calls, more, eligible, err)
	}
	ackCtx, ackCancel := context.WithCancel(context.Background())
	defer ackCancel()
	more, eligible, err = auditSourcePinUnits(ackCtx, context.Background(), func(context.Context) (bool, bool, error) {
		ackCancel()
		return true, true, nil
	})
	if more || !eligible || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation after acknowledged EOF lost the known outcome", more, eligible, err)
	}
}

func TestSourcePinAuditUnknownCommitResumesDurableState(t *testing.T) {
	for _, scenario := range []string{"partial", "repair"} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-committed-%t", scenario, committed), func(t *testing.T) {
				s := testStore(t)
				sourcePinSource(t, s, "a-hot")
				sourcePinSource(t, s, "b-free")
				sourcePinExec(t, s.DB, `UPDATE sources SET created_at='2020-01-01',retention_job_pins=900,retention_preview_pins=900`)
				wantJobs, wantPreviews := int64(0), int64(0)
				if scenario == "partial" {
					wantJobs, wantPreviews = 257, 257
					sourcePinExec(t, s.DB, `INSERT INTO jobs(id,owner,source_id,request,items) SELECT lpad(i::text,4,'0'),'owner','a-hot','{}','[]' FROM generate_series(1,257) i;
 INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) SELECT 'p-'||lpad(i::text,4,'0'),'owner','preview',0,now(),'a-hot' FROM generate_series(1,257) i`)
				}
				before := sourcePinAuditSnapshot(t, s)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				more, eligible, err := auditSourcePinUnits(ctx, ctx, func(ctx context.Context) (bool, bool, error) {
					return s.auditSourcePinUnitCommit(ctx, func(ctx context.Context, tx pgx.Tx) error {
						if committed {
							if err := tx.Commit(ctx); err != nil {
								return err
							}
						} else if err := tx.Rollback(ctx); err != nil {
							return err
						}
						return io.ErrUnexpectedEOF
					})
				})
				if !more || eligible || !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatal("unknown audit COMMIT accepted EOF/repair or lost continuation", more, eligible, err)
				}
				after := sourcePinAuditSnapshot(t, s)
				if committed == (before == after) {
					t.Fatal("actual transaction outcome did not match durable progress", committed, before, after)
				}
				if scenario == "partial" {
					var rows, jobs, previews int
					if err := s.DB.QueryRow(ctx, `SELECT count(*),COALESCE(max(job_count),0),COALESCE(max(preview_count),0) FROM source_pin_audit_progress WHERE source_id='a-hot'`).Scan(&rows, &jobs, &previews); err != nil {
						t.Fatal(err)
					}
					wantRows, wantCount := 0, 0
					if committed {
						wantRows, wantCount = 1, sourcePinAuditChildren
					}
					if rows != wantRows || jobs != wantCount || previews != wantCount {
						t.Fatal("unknown outcome fabricated or lost bounded child checkpoint", rows, jobs, previews)
					}
				} else if committed {
					assertSourcePins(t, s, "a-hot", 0, 0, true)
				} else {
					var jobs int64
					if err := s.DB.QueryRow(ctx, `SELECT retention_job_pins FROM sources WHERE id='a-hot'`).Scan(&jobs); err != nil || jobs != 900 {
						t.Fatal("rollback repaired a source", jobs, err)
					}
				}
				reopened, err := OpenStore(ctx, s.DB.Config().ConnString())
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.DB.Close()
				// At most twelve individually fenced units cover the required wraps.
				// Stop only at acknowledged EOF with no remaining partial children;
				// another sweep can legitimately create a new partial checkpoint.
				completed := false
				for range 12 {
					done, _, err := reopened.auditSourcePinUnit(ctx)
					if err != nil {
						t.Fatal("durable audit did not resume after acknowledgement loss", err)
					}
					if !done {
						continue
					}
					var pending int
					if err := reopened.DB.QueryRow(ctx, `SELECT count(*) FROM source_pin_audit_progress`).Scan(&pending); err != nil {
						t.Fatal(err)
					}
					if pending == 0 {
						completed = true
						break
					}
				}
				if !completed {
					t.Fatal("bounded replay did not acknowledge EOF without partial children")
				}
				assertSourcePins(t, reopened, "a-hot", wantJobs, wantPreviews, true)
				assertSourcePins(t, reopened, "b-free", 0, 0, true)
				var pending int
				if err := reopened.DB.QueryRow(ctx, `SELECT count(*) FROM source_pin_audit_progress`).Scan(&pending); err != nil || pending != 0 {
					t.Fatal("retry retained a partial count or skipped a source", pending, err)
				}
			})
		}
	}
}

func TestSourcePinAuditBlockedAfterMediaCommitKeepsDrainablePaths(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	path := writeLedgerFile(t, c, "sources", "audit-budget", 9)
	if err := s.AddSource(context.Background(), Source{ID: "audit-budget", Owner: "owner", Title: "fixture", Kind: "upload", Path: path, DurationMS: 1000}); err != nil {
		t.Fatal(err)
	}
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at=now()-interval '2 hours'`)
	before := sourcePinAuditSnapshot(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var lock pgx.Tx
	defer func() {
		if lock != nil {
			rollbackStorage(lock)
		}
	}()
	batch, err := s.cleanupStorageBatchCommit(ctx, time.Hour, time.Hour, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		var err error
		lock, err = s.DB.Begin(ctx)
		if err == nil {
			_, err = lock.Exec(ctx, `LOCK TABLE source_retention_audit IN ACCESS EXCLUSIVE MODE`)
		}
		return err
	})
	if err != nil || batch.cacheErr != nil || batch.auditErr != nil || !batch.auditMore || len(batch.paths) != 1 || batch.paths[0] != path || ctx.Err() != nil {
		t.Fatal("owned audit timeout lost committed media paths or became caller failure", batch, err, ctx.Err())
	}
	rollbackStorage(lock)
	lock = nil
	if after := sourcePinAuditSnapshot(t, s); after != before {
		t.Fatal("blocked audit advanced durable progress", before, after)
	}
	var source, pending bool
	if err := s.DB.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM sources WHERE id='audit-budget'),EXISTS(SELECT 1 FROM storage_files WHERE path=$1 AND delete_pending)`, path).Scan(&source, &pending); err != nil || source || !pending {
		t.Fatal("acknowledged media COMMIT was lost", source, pending, err)
	}
	if stored, reserved := ledgerBytes(t, s); stored != 9 || reserved != 0 {
		t.Fatal("audit timeout released undrained charge", stored, reserved)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("audit removed a committed tombstone file", err)
	}
	if err := s.DrainStorageDeletes(context.Background(), c, batch.paths); err != nil {
		t.Fatal(err)
	}
	if more, eligible, err := s.auditSourcePins(context.Background()); err != nil || more || eligible {
		t.Fatal("unblocked empty audit did not acknowledge completion", more, eligible, err)
	}
	if stored, reserved := ledgerBytes(t, s); stored != 0 || reserved != 0 {
		t.Fatal("drain failed to release charge after deferred audit", stored, reserved)
	}
}

func TestSourcePinAuditParentCanceledAfterMediaCommitKeepsDrainablePaths(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	s.ConfigureStorage(c)
	path := writeLedgerFile(t, c, "sources", "audit-parent-cancel", 9)
	if err := s.AddSource(context.Background(), Source{ID: "audit-parent-cancel", Owner: "owner", Title: "fixture", Kind: "upload", Path: path, DurationMS: 1000}); err != nil {
		t.Fatal(err)
	}
	sourcePinExec(t, s.DB, `UPDATE sources SET created_at=now()-interval '2 hours'`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	batch, err := s.cleanupStorageBatchCommit(ctx, time.Hour, time.Hour, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err != nil || !errors.Is(batch.auditErr, context.Canceled) || !batch.auditMore || len(batch.paths) != 1 || batch.paths[0] != path {
		t.Fatal("caller cancellation hid committed paths or audit cancellation", batch, err)
	}
	if stored, reserved := ledgerBytes(t, s); stored != 9 || reserved != 0 {
		t.Fatal("caller cancellation uncharged undrained media", stored, reserved)
	}
	if err := s.DrainStorageDeletes(context.Background(), c, batch.paths); err != nil {
		t.Fatal(err)
	}
	if stored, reserved := ledgerBytes(t, s); stored != 0 || reserved != 0 {
		t.Fatal("retry could not drain after caller cancellation", stored, reserved)
	}
}
