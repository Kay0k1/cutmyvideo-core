package app

// Overlay into both revisions. The same owned fixture measures client-observed
// storage-fence waits/holds without weakening production locks or running media.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type storageDiagnosticOperation struct{}
type storageDiagnosticTraceKey struct{}
type storageDiagnosticQuery struct {
	started         time.Time
	operation       string
	fence, terminal bool
}
type storageDiagnosticHeld struct {
	started   time.Time
	operation string
	waitNS    int64
}
type storageDiagnosticTracer struct {
	mu      sync.Mutex
	held    map[*pgx.Conn]storageDiagnosticHeld
	samples []map[string]any
}

func (d *storageDiagnosticTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	operation, _ := ctx.Value(storageDiagnosticOperation{}).(string)
	text := strings.TrimSpace(strings.ToLower(query.SQL))
	fence := text == "select pg_advisory_xact_lock(736021912360105)"
	if text == "select pg_advisory_xact_lock($1)" && len(query.Args) == 1 {
		value, ok := query.Args[0].(int64)
		fence = ok && value == storageLock
	}
	return context.WithValue(ctx, storageDiagnosticTraceKey{}, storageDiagnosticQuery{started: time.Now(), operation: operation, fence: fence, terminal: text == "commit" || text == "rollback"})
}
func (d *storageDiagnosticTracer) TraceQueryEnd(ctx context.Context, connection *pgx.Conn, result pgx.TraceQueryEndData) {
	query, ok := ctx.Value(storageDiagnosticTraceKey{}).(storageDiagnosticQuery)
	if !ok {
		return
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if query.fence && result.Err == nil && query.operation != "" {
		d.held[connection] = storageDiagnosticHeld{started: now, operation: query.operation, waitNS: now.Sub(query.started).Nanoseconds()}
	}
	if query.terminal {
		if held, ok := d.held[connection]; ok {
			d.samples = append(d.samples, map[string]any{"operation": held.operation, "lock_query_ns": held.waitNS, "acknowledged_hold_ns": now.Sub(held.started).Nanoseconds(), "terminal_success": result.Err == nil})
			delete(d.held, connection)
		}
	}
}

func TestCoreStorageFenceDiagnostic(t *testing.T) {
	output := os.Getenv("CUTMY_CORE_PERF_STORAGE_RESULT")
	if output == "" {
		t.Skip("requires an exclusive task-owned diagnostic result path")
	}
	if os.Getenv("CUTMY_CORE_PERF_IDLE_WINDOW") == "" {
		t.Fatal("coordinator idle window is required")
	}
	var campaigns []map[string]any
	defer func() {
		result := map[string]any{"complete": !t.Failed(), "idle_window": os.Getenv("CUTMY_CORE_PERF_IDLE_WINDOW"), "campaigns": campaigns,
			"scope": "Synthetic owned PostgreSQL namespaces; 1,000 healthy retained artifacts; 1/4 owners, four jobs each, two outputs/job, two processing goroutines, four pool connections. No network/encoding/decoder work. Lock query includes SQL round trip; hold runs from acquisition ACK through COMMIT/ROLLBACK ACK. Pool acquisition is outside those query intervals. Warm bootstrap before timing; no SLA/production capacity claim."}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Error("exclusive diagnostic publication failed", err)
			return
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, owners := range []int{1, 4} {
		s := testStore(t)
		config := s.DB.Config()
		config.MaxConns = 4
		trace := &storageDiagnosticTracer{held: make(map[*pgx.Conn]storageDiagnosticHeld)}
		config.ConnConfig.Tracer = trace
		db, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			t.Fatal("owned traced pool creation failed")
		}
		t.Cleanup(db.Close)
		measured := &Store{DB: db}
		c := ledgerConfig(t)
		c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 256<<20
		c.MaxOutputBytes, c.MaxFetchBytes = 4<<20, 4<<20
		measured.ConfigureStorage(c)
		ctx := context.Background()
		for _, dir := range []string{"sources", "work", "artifacts"} {
			if err := os.MkdirAll(filepath.Join(c.DataDir, dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		sourcePinExec(t, db, `INSERT INTO sources(id,owner,title,duration_ms,kind,url,provider_id)
 SELECT 'history-source-'||n,'history','Retained fixture',10000,'platform','https://media.example/history','history-'||n FROM generate_series(1,1000) n`)
		sourcePinExec(t, db, `INSERT INTO jobs(id,owner,source_id,request,items,status)
 SELECT 'history-job-'||n,'history','history-source-'||n,'{}','[]','succeeded' FROM generate_series(1,1000) n`)
		for n := 1; n <= 1000; n++ {
			writeLedgerFile(t, c, "artifacts", fmt.Sprintf("history-%d", n), 16)
		}
		sourcePinExec(t, db, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,expires_at)
 SELECT 'history-artifact-'||n,'history','history-job-'||n,$1||'/artifacts/history-'||n,'history.bin',16,0,1000,now()+interval '1 hour' FROM generate_series(1,1000) n`, c.DataDir)
		sourcePinExec(t, db, `INSERT INTO storage_files(path,owner,kind,resource_id,size_bytes) SELECT path,owner,'artifact',id,size_bytes FROM artifacts`)
		if err := RunMaintenance(ctx, c, measured); err != nil {
			t.Fatal("owned warm bootstrap failed", err)
		}
		var sources []Source
		for n := 0; n < owners; n++ {
			source := Source{ID: newID("src"), Owner: fmt.Sprintf("owner-%d", n), Title: "Active fixture", Kind: "upload", DurationMS: 10000, Path: writeLedgerFile(t, c, "sources", fmt.Sprintf("active-%d", n), 19)}
			if err := measured.AddSource(ctx, source); err != nil {
				t.Fatal(err)
			}
			sources = append(sources, source)
		}
		var operationsMu sync.Mutex
		var operations []map[string]any
		operation := func(name string, action func(context.Context) error) error {
			opCtx, cancel := context.WithTimeout(context.WithValue(ctx, storageDiagnosticOperation{}, name), 10*time.Second)
			defer cancel()
			start := time.Now()
			err := action(opCtx)
			operationsMu.Lock()
			operations = append(operations, map[string]any{"operation": name, "elapsed_ns": time.Since(start).Nanoseconds(), "success": err == nil})
			operationsMu.Unlock()
			return err
		}
		campaign := map[string]any{"owners": owners, "jobs": owners * 4, "retained_artifacts": 1000, "pool_max_conns": 4, "processing_goroutines": 2}
		campaigns = append(campaigns, campaign)
		start := time.Now()
		failures := make(chan error, owners*4+8)
		var admitted sync.WaitGroup
		for _, source := range sources {
			for range 4 {
				admitted.Add(1)
				go func() {
					defer admitted.Done()
					err := operation("admission", func(opCtx context.Context) error {
						request := requestFor(source)
						request.Ranges = append(request.Ranges, Range{StartMS: 4000, EndMS: 6000})
						_, err := measured.CreateJobLimited(opCtx, source.Owner, request, "", 4, 32)
						return err
					})
					if err != nil {
						failures <- err
					}
				}()
			}
		}
		admitted.Wait()
		var active sync.WaitGroup
		active.Add(3)
		go func() {
			defer active.Done()
			for range 3 {
				if err := operation("maintenance", func(opCtx context.Context) error { return RunMaintenance(opCtx, c, measured) }); err != nil {
					failures <- err
					return
				}
			}
		}()
		for range 2 {
			go func() {
				defer active.Done()
				for {
					var job Job
					var token string
					err := operation("claim", func(opCtx context.Context) error { var err error; job, token, err = measured.Claim(opCtx); return err })
					if errors.Is(err, ErrNotFound) {
						return
					}
					if err != nil {
						failures <- err
						return
					}
					for n := range job.Items {
						item := &job.Items[n]
						item.Status = "running"
						if err := operation("snapshot", func(opCtx context.Context) error { return measured.SaveJob(opCtx, job, token) }); err != nil {
							failures <- err
							return
						}
						artifact := Artifact{ID: newID("art"), Filename: "fixture.bin", SizeBytes: 13, ActualStartMS: item.StartMS, ActualEndMS: item.EndMS}
						path := writeLedgerFile(t, c, "artifacts", artifact.ID, 13)
						item.Status, item.Artifact = "succeeded", &artifact
						if err := operation("publication", func(opCtx context.Context) error { return measured.PublishArtifact(opCtx, job, path, token, artifact) }); err != nil {
							failures <- err
							return
						}
					}
					job.Status, job.Stage = "succeeded", "finished"
					if err := operation("snapshot", func(opCtx context.Context) error { return measured.SaveJob(opCtx, job, token) }); err != nil {
						failures <- err
						return
					}
					if err := operation("release", func(opCtx context.Context) error { return measured.ReleaseJobStorage(opCtx, job.ID, token) }); err != nil {
						failures <- err
						return
					}
				}
			}()
		}
		active.Wait()
		close(failures)
		campaign["elapsed_ns"] = time.Since(start).Nanoseconds()
		campaign["operations"] = operations
		trace.mu.Lock()
		campaign["storage_fences"] = trace.samples
		remaining := len(trace.held)
		trace.mu.Unlock()
		for err := range failures {
			t.Error("owned diagnostic operation failed", err)
		}
		if remaining != 0 {
			t.Error("diagnostic retained an unacknowledged fence", remaining)
		}
		var complete int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE owner<>'history' AND status='succeeded'`).Scan(&complete); err != nil || complete != owners*4 {
			t.Error("diagnostic jobs incomplete", complete, err)
		}
		_, reserved := ledgerBytes(t, measured)
		if reserved != 0 {
			t.Error("completed diagnostic retained reservations", reserved)
		}
		if t.Failed() {
			return
		}
	}
}
