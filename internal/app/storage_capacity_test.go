package app

// This opt-in workload uses only a fresh, owned PostgreSQL namespace and tiny
// synthetic files. It measures retained-row maintenance, not media encoding.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type capacityLatency struct {
	SamplesMS []float64 `json:"samples_ms"`
	P50MS     float64   `json:"p50_ms"`
	P95MS     float64   `json:"p95_ms"`
	MaxMS     float64   `json:"max_ms"`
}

func summarizeCapacityLatency(values []float64) capacityLatency {
	result := capacityLatency{SamplesMS: values}
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	if len(ordered) != 0 {
		result.P50MS = ordered[int(math.Ceil(float64(len(ordered))*.50))-1]
		result.P95MS = ordered[int(math.Ceil(float64(len(ordered))*.95))-1]
		result.MaxMS = ordered[len(ordered)-1]
	}
	return result
}

type capacityMemory struct {
	PeakHeapAllocBytes uint64 `json:"peak_heap_alloc_bytes"`
	PeakGoSysBytes     uint64 `json:"peak_go_sys_bytes"`
	PeakRSSBytes       uint64 `json:"sampled_peak_process_rss_bytes"`
	TotalAllocBytes    uint64 `json:"total_alloc_delta_bytes"`
	NumGC              uint32 `json:"gc_delta"`
}

type capacityQuery struct {
	Name      string            `json:"name"`
	SQL       string            `json:"sql"`
	Client    capacityLatency   `json:"client_round_trip"`
	Execution capacityLatency   `json:"postgres_execution"`
	Plans     []json.RawMessage `json:"explain_analyze_buffers"`
}

type capacityReport struct {
	FixtureRows        int             `json:"retained_rows_per_primary_table"`
	Schema             string          `json:"owned_schema"`
	PostgreSQL         string          `json:"postgresql"`
	DatabaseBytes      int64           `json:"owned_schema_relation_bytes"`
	FinalDatabaseBytes int64           `json:"final_owned_schema_relation_bytes"`
	PostgreSQLSettings json.RawMessage `json:"postgresql_settings"`
	Go                 string          `json:"go"`
	OS                 string          `json:"os"`
	Arch               string          `json:"arch"`
	GOMAXPROCS         int             `json:"gomaxprocs"`
	Started            string          `json:"started_at"`
	DurationMS         float64         `json:"duration_ms"`
	Queries            []capacityQuery `json:"queries"`
	Workloads          map[string]any  `json:"workloads"`
	Memory             capacityMemory  `json:"go_process_memory"`
	Failures           []string        `json:"failures"`
}

type capacityFixture struct {
	admin            *pgxpool.Pool
	api, maintenance *Store
	c                Config
	schema           string
	retained         int
}

func capacityExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func newCapacityFixture(t *testing.T, retained int) capacityFixture {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("capacity workload requires TEST_DATABASE_URL to an isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal("invalid dedicated test database configuration")
	}
	schema := newID("capacity")
	capacityExec(t, admin, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	f := capacityFixture{admin: admin, schema: schema, retained: retained}
	t.Cleanup(func() {
		if f.api != nil {
			f.api.DB.Close()
		}
		if f.maintenance != nil {
			f.maintenance.DB.Close()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := admin.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error("owned capacity schema cleanup failed", err)
		}
	})
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("capacity workload requires a PostgreSQL URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	q.Set("pool_max_conns", "4")
	q.Set("application_name", schema+"_api")
	u.RawQuery = q.Encode()
	f.api, err = OpenStore(ctx, u.String())
	if err != nil {
		t.Fatal("capacity fixture startup failed", err)
	}
	q.Set("application_name", schema+"_maintenance")
	u.RawQuery = q.Encode()
	f.maintenance, err = OpenStore(ctx, u.String())
	if err != nil {
		t.Fatal("capacity maintenance startup failed", err)
	}
	f.c = Config{DataDir: t.TempDir(), MaxStorageBytes: 1 << 30, MaxSourceBytes: 4096, MaxOwnerBytes: 1 << 20,
		SourceTimeout: time.Minute, JobTimeout: time.Minute, SourceTTL: time.Hour, ArtifactTTL: time.Hour}
	for _, name := range []string{"sources", "artifacts", "work", "preview"} {
		if err = os.MkdirAll(filepath.Join(f.c.DataDir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.api.ConfigureStorage(f.c)
	f.maintenance.ConfigureStorage(f.c)
	// Retained remote-source metadata has ten sources per owner, one job and
	// artifact each. One in five jobs remains queued and pins its resources.
	// Explicitly expired artifacts are interleaved through the retained rows.
	capacityExec(t, f.api.DB, `INSERT INTO sources(id,owner,title,duration_ms,kind,url,provider_id,created_at)
 SELECT 'retained-source-'||i,'retained-owner-'||(i/10),'Synthetic retained source',1200000,'platform',
 'https://www.youtube.com/watch?v=fixture'||i,'fixture',now()-interval '2 hours' FROM generate_series(1,$1) i`, retained)
	capacityExec(t, f.api.DB, `INSERT INTO jobs(id,owner,source_id,request,items,status,created_at,updated_at)
 SELECT 'retained-job-'||i,'retained-owner-'||(i/10),'retained-source-'||i,'{}','[]',
 CASE WHEN i%5=0 THEN 'queued' ELSE 'succeeded' END,now()-interval '2 hours',now()
 FROM generate_series(1,$1) i`, retained)
	capacityExec(t, f.api.DB, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,created_at,expires_at)
 SELECT 'retained-artifact-'||i,'retained-owner-'||(i/10),'retained-job-'||i,$2||'/artifacts/retained-'||i,
 'synthetic.mp4',16,0,1000,now()-interval '2 hours',CASE WHEN i%100=1 THEN now()-interval '1 hour' ELSE now()+interval '1 day' END
 FROM generate_series(1,$1) i`, retained, f.c.DataDir)
	capacityExec(t, f.api.DB, `INSERT INTO sources(id,owner,title,duration_ms,kind,path,created_at)
 SELECT 'expired-source-'||i,'expiry-owner-'||i,'Expired synthetic source',1000,'upload',$2||'/sources/expired-'||i,
 now()-interval '2 hours' FROM generate_series(1,$1) i`, max(storageBatch, retained/100), f.c.DataDir)
	capacityExec(t, f.api.DB, `INSERT INTO jobs(id,owner,source_id,request,items,status,created_at,updated_at)
 SELECT 'expired-job-'||i,'retained-owner-'||(i/10),'retained-source-'||i,'{}','[]','succeeded',now()-interval '2 hours',now()-interval '2 hours'
 FROM generate_series(1,$1) i`, max(storageBatch, retained/100))
	capacityExec(t, f.api.DB, `INSERT INTO storage_files(path,owner,kind,resource_id,size_bytes)
 SELECT path,owner,'artifact',id,size_bytes FROM artifacts;
 INSERT INTO storage_files(path,owner,kind,resource_id,size_bytes) SELECT path,owner,'source',id,16 FROM sources WHERE path<>''`)
	// Small cached JSON payloads isolate retained-row lookup/deletion costs.
	// Encoding/extractor/network work is deliberately outside this workload.
	source, info := metadataCacheFixture()
	info, _, err = cacheablePlatformMetadata(source, info, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := encodePlatformMetadata(info)
	if err != nil {
		t.Fatal(err)
	}
	capacityExec(t, f.api.DB, `INSERT INTO source_metadata_cache(source_id,owner,payload,expires_at)
	 SELECT id,owner,$1,CASE WHEN substring(id from '[0-9]+$')::integer%100=2 THEN now()-interval '1 hour' ELSE now()+interval '5 minutes' END
 FROM sources WHERE kind='platform'`, payload)
	capacityExec(t, f.api.DB, `INSERT INTO storage_files(path,kind,size_bytes,observed_at)
 SELECT $2||'/artifacts/orphan-'||i,'orphan',16,now()-interval '2 days' FROM generate_series(1,$1) i`, max(storageBatch, retained/20), f.c.DataDir)
	capacityExec(t, f.api.DB, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id)
 SELECT 'preview-reservation-'||i,'retained-owner-'||(i/10),'preview',16,now()+interval '1 day','retained-source-'||i
 FROM generate_series(1,$1) i`, max(storageBatch, retained/100))
	// Mark bootstrap complete so admissions measure steady state, rather than
	// the separate installation-wide legacy adoption scan.
	capacityExec(t, f.api.DB, `INSERT INTO storage_state(id) VALUES($1)`, f.c.DataDir)
	capacityExec(t, f.api.DB, `ANALYZE`)
	return f
}

// Read current runtime SQL literals with Go's parser. A changed query is
// measured as changed SQL, and a moved/dynamic query fails until reviewed.
func capacityRuntimeSQL(t *testing.T, file, function, prefix string) string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	functions := map[string]*ast.FuncDecl{}
	for _, declaration := range parsed.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if ok {
			functions[fn.Name.Name] = fn
		}
	}
	visited := map[string]bool{}
	var inspect func(string)
	inspect = func(name string) {
		fn := functions[name]
		if fn == nil || visited[name] {
			return
		}
		visited[name] = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			inspect(selector.Sel.Name)
			if len(call.Args) < 2 || !slices.Contains([]string{"Exec", "Query", "QueryRow"}, selector.Sel.Name) {
				return true
			}
			literal, ok := call.Args[1].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(value, prefix) {
				found = append(found, value)
			}
			return true
		})
	}
	inspect(function)
	if len(found) != 1 {
		t.Fatalf("runtime query selector %s/%s/%s matched %d statements", file, function, prefix, len(found))
	}
	return found[0]
}

func capacityMeasureQuery(t *testing.T, f capacityFixture, name, sql string, args []any, repeats int) capacityQuery {
	t.Helper()
	q := capacityQuery{Name: name, SQL: sql}
	client, execution := []float64{}, []float64{}
	for i := 0; i < repeats; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tx, err := f.api.DB.Begin(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		// SQL mutation plans are rolled back; all iterations see the same
		// eligible rows. ANALYZE executes triggers and real DML/FK checks.
		start := time.Now()
		var raw []byte
		err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		rollbackStorage(tx)
		cancel()
		if err != nil {
			t.Fatal(name, err)
		}
		var plan []struct {
			ExecutionTime float64 `json:"Execution Time"`
		}
		if err = json.Unmarshal(raw, &plan); err != nil || len(plan) != 1 {
			t.Fatal("invalid EXPLAIN JSON", err)
		}
		client = append(client, elapsed)
		execution = append(execution, plan[0].ExecutionTime)
		q.Plans = append(q.Plans, json.RawMessage(raw))
	}
	q.Client = summarizeCapacityLatency(client)
	q.Execution = summarizeCapacityLatency(execution)
	return q
}

func capacityPlans(t *testing.T, f capacityFixture, repeats int) []capacityQuery {
	queries := []struct {
		name, file, function, prefix string
		args                         []any
	}{
		{"artifact_retention", "storage_lifecycle.go", "cleanupStorageBatch", "DELETE FROM artifacts", []any{float64(3600), storageBatch}},
		{"job_retention", "storage_lifecycle.go", "cleanupStorageBatch", "DELETE FROM jobs", []any{float64(3600), storageBatch}},
		{"source_retention", "storage_lifecycle.go", "cleanupStorageBatch", "DELETE FROM sources", []any{float64(3600), storageBatch}},
		{"pending_tombstones", "storage_lifecycle.go", "pendingStoragePaths", "SELECT path", []any{storageBatch}},
		{"orphan_retention", "storage_lifecycle.go", "ReconcileStorage", "SET delete_pending=true", []any{float64(7200), storageBatch}},
		{"cache_retention", "storage_lifecycle.go", "cleanupStorageBatch", "DELETE FROM source_metadata_cache", nil},
		{"cache_recent_hit", "metadata_cache.go", "RecentPlatformMetadata", "SELECT source.id", []any{"retained-owner-0", "https://www.youtube.com/watch?v=fixture1"}},
		{"cache_recent_miss", "metadata_cache.go", "RecentPlatformMetadata", "SELECT source.id", []any{"retained-owner-0", "https://www.youtube.com/watch?v=not-retained"}},
		{"storage_admission_totals", "storage_ledger.go", "storageFits", "SELECT stored_bytes", []any{"", int64(4096), f.c.MaxStorageBytes}},
		{"active_source_reservations", "storage_ledger.go", "reserveSource", "SELECT count(*) FROM storage_reservations", nil},
	}
	var result []capacityQuery
	for _, q := range queries {
		sql := capacityRuntimeSQL(t, q.file, q.function, q.prefix)
		args := q.args
		if q.name == "cache_retention" && strings.Contains(sql, "$1") {
			args = []any{storageBatch}
		}
		result = append(result, capacityMeasureQuery(t, f, q.name, sql, args, repeats))
	}
	return result
}

func capacityCount(t *testing.T, db *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func capacityPoisonDeletes(t *testing.T, f capacityFixture) map[string]any {
	t.Helper()
	paths := make([]string, 0, storageBatch*2)
	for i := 0; i < storageBatch*2; i++ {
		name := fmt.Sprintf("poison-%03d", i)
		if i >= storageBatch {
			name = fmt.Sprintf("safe-%03d", i)
		}
		path := filepath.Join(f.c.DataDir, "sources", name)
		if i < storageBatch {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	capacityExec(t, f.api.DB, `INSERT INTO storage_files(path,kind,size_bytes,delete_pending) SELECT path,'tombstone',9,true FROM unnest($1::text[]) AS f(path)`, paths)
	latencies := []float64{}
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		start := time.Now()
		tx, err := f.maintenance.storageTx(ctx)
		var pending []string
		if err == nil {
			pending, err = pendingStoragePaths(ctx, tx)
			if err == nil {
				err = tx.Commit(ctx)
			}
			rollbackStorage(tx)
		}
		if err == nil {
			err = f.maintenance.DrainStorageDeletes(ctx, f.c, pending)
		}
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000)
		cancel()
		// Once healthy paths pass deferred poison rows a cycle can succeed.
		// The invariant below, rather than an error-string match, proves safety.
	}
	poison := capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_files WHERE path=ANY($1)", paths[:storageBatch])
	safe := capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_files WHERE path=ANY($1)", paths[storageBatch:])
	physicalSafe := 0
	for _, path := range paths[storageBatch:] {
		if _, err := os.Lstat(path); err == nil {
			physicalSafe++
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	charged := capacityCount(t, f.api.DB, "SELECT COALESCE(sum(size_bytes),0) FROM storage_files WHERE path=ANY($1)", paths[:storageBatch])
	for _, path := range paths[:storageBatch] {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatal("poison directory was removed")
		}
	}
	capacityExec(t, f.api.DB, "DELETE FROM storage_files WHERE path=ANY($1)", paths)
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	return map[string]any{"poison_count": storageBatch, "safe_count": storageBatch, "cycles": 3, "latency": summarizeCapacityLatency(latencies), "poison_rows_preserved": poison, "poison_bytes_still_charged": charged, "safe_rows_remaining": safe, "safe_physical_files_remaining": physicalSafe, "pass": poison == storageBatch && charged == storageBatch*9 && safe == 0 && physicalSafe == 0}
}

func capacityFullReconcile(t *testing.T, f capacityFixture, pattern string, expected int) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var progressTable bool
	if err := f.api.DB.QueryRow(ctx, "SELECT to_regclass('storage_scan_progress') IS NOT NULL").Scan(&progressTable); err != nil {
		t.Fatal(err)
	}
	for calls := 1; calls <= 3000; calls++ {
		if err := f.maintenance.ReconcileStorage(ctx, f.c); err != nil {
			t.Fatal(err)
		}
		complete := true
		if progressTable {
			var n int
			if err := f.api.DB.QueryRow(ctx, "SELECT count(*) FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind IN ('reconcile_sources','reconcile_artifacts') AND completed_at IS NOT NULL", f.c.DataDir).Scan(&n); err != nil {
				t.Fatal(err)
			}
			complete = n == 2
		}
		if complete && capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_files WHERE path LIKE $1", pattern) == int64(expected) {
			return calls
		}
	}
	t.Fatal("public reconciliation failed to finish its committed generation")
	return 0
}

func capacityShortReconciliation(t *testing.T, f capacityFixture) map[string]any {
	t.Helper()
	const smallFiles = 1024
	count := f.retained + smallFiles
	for i := 0; i < count; i++ {
		path := filepath.Join(f.c.DataDir, "artifacts", fmt.Sprintf("scan-%06d", i))
		var content []byte
		if i < smallFiles {
			content = []byte("synthetic")
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pattern := filepath.Join(f.c.DataDir, "artifacts", "scan-%")
	latencies := []float64{}
	progress := []int64{}
	timeouts := 0
	for range 32 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		start := time.Now()
		err := f.maintenance.ReconcileStorage(ctx, f.c)
		cancel()
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000)
		if errors.Is(err, context.DeadlineExceeded) {
			timeouts++
		} else if err != nil {
			t.Fatal(err)
		}
		progress = append(progress, capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_files WHERE path LIKE $1", pattern))
	}
	// Final unbounded scan verifies data/accounting independently of whether
	// short cycles progress. The budgeted result is not silently repaired.
	before := progress[len(progress)-1]
	fullStart := time.Now()
	fullCalls := capacityFullReconcile(t, f, pattern, count)
	fullMS := float64(time.Since(fullStart).Microseconds()) / 1000
	if n := capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_files WHERE path LIKE $1", pattern); n != int64(count) {
		t.Fatal("full reconciliation lost files", n)
	}
	var xminBefore, xminAfter string
	if err := f.api.DB.QueryRow(context.Background(), "SELECT md5(string_agg(path||xmin::text,',' ORDER BY path)) FROM storage_files WHERE path LIKE $1", pattern).Scan(&xminBefore); err != nil {
		t.Fatal(err)
	}
	unchangedStart := time.Now()
	unchangedCalls := capacityFullReconcile(t, f, pattern, count)
	unchangedMS := float64(time.Since(unchangedStart).Microseconds()) / 1000
	if err := f.api.DB.QueryRow(context.Background(), "SELECT md5(string_agg(path||xmin::text,',' ORDER BY path)) FROM storage_files WHERE path LIKE $1", pattern).Scan(&xminAfter); err != nil {
		t.Fatal(err)
	}
	progressed := before == int64(count) || before >= progress[7]+min(int64(count)-progress[7], int64(storageScanBatchSize*4))
	return map[string]any{"physical_zero_byte_entries": f.retained, "physical_small_files": smallFiles, "physical_files": count, "cycle_budget_ms": 5, "cycles": 32, "timeouts": timeouts, "latency": summarizeCapacityLatency(latencies), "registered_after_each_cycle": progress, "registered_before_full_scan": before, "final_full_scan_calls": fullCalls, "unchanged_full_scan_calls": unchangedCalls, "unchanged_full_scan_ms": unchangedMS, "final_full_scan_ms": fullMS, "unchanged_scan_preserves_tuple_xmin": xminBefore == xminAfter, "pass": progressed && xminBefore == xminAfter}
}

func capacityBootstrap(t *testing.T, f capacityFixture) map[string]any {
	t.Helper()
	pattern := filepath.Join(f.c.DataDir, "artifacts", "scan-%")
	capacityExec(t, f.api.DB, "DELETE FROM storage_state WHERE id=$1", f.c.DataDir)
	capacityExec(t, f.api.DB, "DELETE FROM storage_files WHERE path LIKE $1", pattern)
	progress := []int64{}
	latencies := []float64{}
	deferrals := 0
	for range 32 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		begin := time.Now()
		err := f.api.ReserveSource(ctx, f.c, "bootstrap-owner", "bootstrap-token")
		cancel()
		latencies = append(latencies, float64(time.Since(begin).Microseconds())/1000)
		if err != nil {
			deferrals++
		} else {
			if err := f.api.ReleaseSource(context.Background(), "bootstrap-owner", "bootstrap-token"); err != nil {
				t.Fatal(err)
			}
		}
		progress = append(progress, capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_files WHERE path LIKE $1", pattern))
	}
	before := progress[len(progress)-1]
	// A progressive implementation may intentionally return initializing.
	// Retry real admissions within a fixed aggregate budget; no ledger patching.
	begin := time.Now()
	finalCalls := 0
	complete := false
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for finalCalls < 3000 && ctx.Err() == nil {
		finalCalls++
		err := f.api.ReserveSource(ctx, f.c, "bootstrap-owner", "bootstrap-token")
		if err == nil {
			complete = true
			if err = f.api.ReleaseSource(ctx, "bootstrap-owner", "bootstrap-token"); err != nil {
				t.Fatal(err)
			}
			break
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			break
		}
	}
	final := capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_files WHERE path LIKE $1", pattern)
	marker := capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_state WHERE id=$1", f.c.DataDir)
	return map[string]any{"cycle_budget_ms": 5, "cycles": 32, "latency": summarizeCapacityLatency(latencies), "deferrals": deferrals, "registered_after_each_cycle": progress, "registered_before_final_pass": before, "final_retry_calls": finalCalls, "final_pass_ms": float64(time.Since(begin).Microseconds()) / 1000, "final_registered_files": final, "initialization_markers": marker, "pass": complete && marker == 1 && final == int64(f.retained+1024) && (before > progress[0] || before == int64(f.retained+1024))}
}

func capacityConcurrent(t *testing.T, f capacityFixture) map[string]any {
	t.Helper()
	// Refresh through the real writer before timed calls so longer plan/scan
	// experiments do not turn the five-minute cache TTL into a load failure.
	source, info := metadataCacheFixture()
	source.ID, source.Owner, source.URL = "retained-source-1", "retained-owner-0", "https://www.youtube.com/watch?v=fixture1"
	if err := f.api.CachePlatformMetadata(context.Background(), source, info); err != nil {
		t.Fatal(err)
	}
	const workers, operations = 4, 20
	start := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, workers+2)
	var group sync.WaitGroup
	var mu sync.Mutex
	reserve, publish, cleanup, cache := []float64{}, []float64{}, []float64{}, []float64{}
	var lockSamples, blockedSamples, maxBlocked int
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				var n int
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				err := f.admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE $1 AND wait_event_type='Lock' AND wait_event='advisory'", f.schema+"_%").Scan(&n)
				cancel()
				if err == nil {
					lockSamples++
					if n > 0 {
						blockedSamples++
					}
					maxBlocked = max(maxBlocked, n)
				}
			}
		}
	}()
	for worker := range workers {
		group.Go(func() {
			<-start
			for operation := range operations {
				owner := fmt.Sprintf("capacity-owner-%d-%d", worker, operation)
				id := fmt.Sprintf("capacity-published-%d-%d", worker, operation)
				token := id
				path := filepath.Join(f.c.DataDir, "sources", id)
				if err := os.WriteFile(path, []byte("synthetic completed file"), 0600); err != nil {
					errs <- err
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				begin := time.Now()
				err := f.api.ReserveSource(ctx, f.c, owner, token)
				elapsed := float64(time.Since(begin).Microseconds()) / 1000
				mu.Lock()
				reserve = append(reserve, elapsed)
				mu.Unlock()
				if err != nil {
					cancel()
					errs <- err
					return
				}
				begin = time.Now()
				err = f.api.AddSource(ctx, Source{ID: id, Owner: owner, Title: "Synthetic publication", Kind: "upload", Path: path, DurationMS: 1000, StorageToken: token})
				elapsed = float64(time.Since(begin).Microseconds()) / 1000
				cancel()
				mu.Lock()
				publish = append(publish, elapsed)
				mu.Unlock()
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	group.Go(func() {
		<-start
		for range operations {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			begin := time.Now()
			paths, err := f.maintenance.Cleanup(ctx, time.Hour, time.Hour)
			// Cleanup can return committed paths together with a cache-stage
			// failure. Drain those paths before reporting either cause.
			err = errors.Join(err, f.maintenance.DrainStorageDeletes(ctx, f.c, paths))
			elapsed := float64(time.Since(begin).Microseconds()) / 1000
			cancel()
			mu.Lock()
			cleanup = append(cleanup, elapsed)
			mu.Unlock()
			if err != nil {
				errs <- err
				return
			}
		}
	})
	group.Go(func() {
		<-start
		for range operations * workers {
			begin := time.Now()
			_, hit := f.api.RecentPlatformMetadata(context.Background(), "retained-owner-0", "https://www.youtube.com/watch?v=fixture1")
			elapsed := float64(time.Since(begin).Microseconds()) / 1000
			mu.Lock()
			cache = append(cache, elapsed)
			mu.Unlock()
			if !hit {
				errs <- errors.New("owner-scoped synthetic metadata cache miss under load")
				return
			}
		}
	})
	begin := time.Now()
	close(start)
	group.Wait()
	close(done)
	<-monitorDone
	close(errs)
	var failures []string
	for err := range errs {
		failures = append(failures, err.Error())
	}
	count := capacityCount(t, f.api.DB, "SELECT count(*) FROM sources WHERE id LIKE 'capacity-published-%'")
	stored := capacityCount(t, f.api.DB, "SELECT stored_bytes::bigint FROM storage_counters WHERE id=1")
	files := capacityCount(t, f.api.DB, "SELECT COALESCE(sum(size_bytes),0) FROM storage_files")
	reserved := capacityCount(t, f.api.DB, "SELECT reserved_bytes::bigint FROM storage_counters WHERE id=1")
	reservations := capacityCount(t, f.api.DB, "SELECT COALESCE(sum(size_bytes),0) FROM storage_reservations")
	active := capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_reservations WHERE kind='source'")
	return map[string]any{"admission_workers": workers, "operations_per_worker": operations, "duration_ms": float64(time.Since(begin).Microseconds()) / 1000,
		"reserve_source": summarizeCapacityLatency(reserve), "durable_source_publication": summarizeCapacityLatency(publish), "retention_batch": summarizeCapacityLatency(cleanup), "persisted_metadata_hit": summarizeCapacityLatency(cache),
		"advisory_lock_samples": lockSamples, "samples_with_waiters": blockedSamples, "max_observed_waiters": maxBlocked, "sampling_interval_ms": 5,
		"published_sources": count, "source_reservations_remaining": active, "counters_match_actual_ledger": stored == files && reserved == reservations, "errors": failures,
		"pass": len(failures) == 0 && count == workers*operations && active == 0 && stored == files && reserved == reservations}
}

func capacityLegacyNullDeadlines(t *testing.T, f capacityFixture) map[string]any {
	t.Helper()
	current := capacityCount(t, f.api.DB, "SELECT count(*) FROM artifacts")
	missing := int64(f.retained) - current
	if missing < 0 {
		t.Fatal("unexpected artifact fixture growth")
	}
	if missing > 0 {
		capacityExec(t, f.api.DB, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,created_at)
 SELECT 'legacy-null-extra-'||i,'retained-owner-0','retained-job-1',$2||'/artifacts/legacy-null-extra-'||i,'synthetic.mp4',16,0,1000,now()-interval '2 hours' FROM generate_series(1,$1) i`, missing, f.c.DataDir)
		capacityExec(t, f.api.DB, `INSERT INTO storage_files(path,owner,kind,resource_id,size_bytes) SELECT path,owner,'artifact',id,size_bytes FROM artifacts WHERE id LIKE 'legacy-null-extra-%'`)
	}
	// Restore the actual pre-deadline metadata representation without doing
	// the backfill itself. Fresh bootstrap metadata exists only in this owned
	// fixture; all application rows and real scan files remain available.
	capacityExec(t, f.api.DB, "UPDATE artifacts SET expires_at=NULL")
	capacityExec(t, f.api.DB, "VACUUM (ANALYZE) artifacts")
	capacityExec(t, f.api.DB, "DELETE FROM storage_state WHERE id=$1", f.c.DataDir)
	var progressTable bool
	if err := f.api.DB.QueryRow(context.Background(), "SELECT to_regclass('storage_scan_progress') IS NOT NULL").Scan(&progressTable); err != nil {
		t.Fatal(err)
	}
	if progressTable {
		capacityExec(t, f.api.DB, "DELETE FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind IN ('bootstrap_sources','bootstrap_artifacts','bootstrap_work')", f.c.DataDir)
	}
	initial := capacityCount(t, f.api.DB, "SELECT count(*) FROM artifacts WHERE expires_at IS NULL")
	if initial != int64(f.retained) {
		t.Fatal("legacy deadline fixture has wrong size", initial)
	}
	remaining := []int64{}
	latencies := []float64{}
	timeouts := 0
	deferrals := 0
	complete := false
	begin := time.Now()
	overall, cancelOverall := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelOverall()
	for calls := 0; calls < 3000 && overall.Err() == nil; calls++ {
		// Five seconds is the admission's database budget. Retrying a single
		// long transaction with a larger budget would hide the old failure.
		ctx, cancel := context.WithTimeout(overall, 5*time.Second)
		start := time.Now()
		err := f.api.ReserveSource(ctx, f.c, "legacy-deadline-owner", "legacy-deadline-token")
		cancel()
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000)
		remaining = append(remaining, capacityCount(t, f.api.DB, "SELECT count(*) FROM artifacts WHERE expires_at IS NULL"))
		if err == nil {
			complete = true
			if err = f.api.ReleaseSource(context.Background(), "legacy-deadline-owner", "legacy-deadline-token"); err != nil {
				t.Fatal(err)
			}
			break
		}
		if errors.Is(err, context.DeadlineExceeded) {
			timeouts++
		} else {
			deferrals++
		}
	}
	marker := capacityCount(t, f.api.DB, "SELECT count(*) FROM storage_state WHERE id=$1", f.c.DataDir)
	final := capacityCount(t, f.api.DB, "SELECT count(*) FROM artifacts WHERE expires_at IS NULL")
	stored := capacityCount(t, f.api.DB, "SELECT stored_bytes::bigint FROM storage_counters WHERE id=1")
	files := capacityCount(t, f.api.DB, "SELECT COALESCE(sum(size_bytes),0) FROM storage_files")
	return map[string]any{"initial_null_deadlines": initial, "remaining_after_each_admission": remaining, "call_budget_ms": 5000, "aggregate_budget_ms": 120000, "admission_calls": len(latencies), "admission_latency": summarizeCapacityLatency(latencies), "timeouts": timeouts, "initializing_deferrals": deferrals, "duration_ms": float64(time.Since(begin).Microseconds()) / 1000, "initialization_markers": marker, "remaining_null_deadlines": final, "counters_match_actual_ledger": stored == files, "pass": complete && marker == 1 && final == 0 && stored == files}
}

func monitorCapacityMemory() func() capacityMemory {
	var start runtime.MemStats
	runtime.ReadMemStats(&start)
	result := capacityMemory{}
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		sample := func() {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			result.PeakHeapAllocBytes = max(result.PeakHeapAllocBytes, m.HeapAlloc)
			result.PeakGoSysBytes = max(result.PeakGoSysBytes, m.Sys)
			if data, err := os.ReadFile("/proc/self/status"); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "VmRSS:") {
						fields := strings.Fields(line)
						if len(fields) >= 2 {
							kb, _ := strconv.ParseUint(fields[1], 10, 64)
							result.PeakRSSBytes = max(result.PeakRSSBytes, kb*1024)
						}
					}
				}
			}
		}
		sample()
		for {
			select {
			case <-stop:
				sample()
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	return func() capacityMemory {
		once.Do(func() {
			close(stop)
			<-done
			var end runtime.MemStats
			runtime.ReadMemStats(&end)
			result.TotalAllocBytes = end.TotalAlloc - start.TotalAlloc
			result.NumGC = end.NumGC - start.NumGC
		})
		return result
	}
}

// Run last so a large expired cache cannot change the earlier retained-row
// plans or contention fixture. Fresh dedicated sources prevent cascade deletion
// from masquerading as cache expiry progress. Public Cleanup exists on both
// compared versions and preserves all stage failures on the revised runtime.
func capacityCacheBacklog(t *testing.T, f capacityFixture) map[string]any {
	capacityExec(t, f.api.DB, `INSERT INTO sources(id,owner,title,duration_ms,kind,url,provider_id,created_at)
 SELECT 'cache-capacity-'||lpad(i::text,8,'0'),'cache-owner-'||(i%2),'cache capacity fixture',1000,
 'platform','https://www.youtube.com/watch?v=fixture'||i,'fixture'||i,now()
 FROM generate_series(1,$1::integer+23) i`, f.retained)
	capacityExec(t, f.api.DB, `INSERT INTO source_metadata_cache(source_id,owner,payload,expires_at)
 SELECT id,owner,convert_to('payload-'||id,'UTF8'),
 CASE WHEN substring(id from '[0-9]+$')::integer<=$1 THEN now()-interval '2 hours' ELSE now()+interval '1 day' END
 FROM sources WHERE id LIKE 'cache-capacity-%'`, f.retained)
	liveSnapshot := func() string {
		var value string
		if err := f.api.DB.QueryRow(context.Background(), `SELECT jsonb_agg(jsonb_build_object('row',to_jsonb(c),'xmin',c.xmin::text) ORDER BY source_id)::text
 FROM source_metadata_cache c WHERE source_id LIKE 'cache-capacity-%' AND expires_at>statement_timestamp()`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	beforeLive := liveSnapshot()
	expiredCount := func() int64 {
		return capacityCount(t, f.api.DB, `SELECT count(*) FROM source_metadata_cache WHERE source_id LIKE 'cache-capacity-%' AND expires_at<=statement_timestamp()`)
	}
	before := expiredCount()
	if before != int64(f.retained) {
		t.Fatal("cache backlog fixture did not create the exact expired row count", before)
	}
	var calls []map[string]any
	var latencies []float64
	var failures []string
	previous := before
	maxRemoved := int64(0)
	for cycle := 1; cycle <= 8; cycle++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		_, err := f.maintenance.Cleanup(ctx, f.c.ArtifactTTL, f.c.SourceTTL)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		cancel()
		remaining := expiredCount()
		removed := previous - remaining
		maxRemoved = max(maxRemoved, removed)
		call := map[string]any{"cycle": cycle, "elapsed_ms": elapsed, "expired_remaining": remaining, "removed": removed}
		if err != nil {
			call["error"] = err.Error()
			failures = append(failures, err.Error())
		}
		calls = append(calls, call)
		latencies = append(latencies, elapsed)
		previous = remaining
	}
	sources := capacityCount(t, f.api.DB, `SELECT count(*) FROM sources WHERE id LIKE 'cache-capacity-%'`)
	liveUnchanged := beforeLive == liveSnapshot()
	removed := before - previous
	return map[string]any{
		"pass":            len(failures) == 0 && maxRemoved <= storageBatch && removed == min(before, 8*storageBatch) && liveUnchanged && sources == before+23,
		"initial_expired": before, "final_expired": previous, "total_removed": removed, "max_removed_per_call": maxRemoved,
		"fresh_control_rows": 23, "fresh_identity_payload_deadline_xmin_unchanged": liveUnchanged, "dedicated_sources_remaining": sources,
		"batch_limit": storageBatch, "calls": calls, "latency": summarizeCapacityLatency(latencies), "errors": failures,
		"scope": "Eight public Cleanup calls; fresh dedicated sources protect cache rows from source cascades. Does not require draining the complete backlog.",
	}
}

func TestStorageCapacityEvidence(t *testing.T) {
	if os.Getenv("CUTMY_STORAGE_CAPACITY") != "1" {
		t.Skip("opt in with make storage-capacity-check using a dedicated PostgreSQL database")
	}
	output := os.Getenv("CUTMY_STORAGE_REPORT_DIR")
	if !filepath.IsAbs(output) {
		t.Fatal("CUTMY_STORAGE_REPORT_DIR must be absolute")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	sizes := os.Getenv("CUTMY_STORAGE_SIZES")
	if sizes == "" {
		sizes = "10000,100000"
	}
	repeats := 20
	if raw := os.Getenv("CUTMY_STORAGE_SAMPLES"); raw != "" {
		var err error
		repeats, err = strconv.Atoi(raw)
		if err != nil || repeats < 3 || repeats > 100 {
			t.Fatal("samples must be 3..100")
		}
	}
	for _, raw := range strings.Split(sizes, ",") {
		count, err := strconv.Atoi(raw)
		if err != nil || count < 1000 || count > 100000 {
			t.Fatal("retained row sizes must be 1000..100000")
		}
		t.Run(raw, func(t *testing.T) {
			begin := time.Now()
			stopMemory := monitorCapacityMemory()
			defer stopMemory()
			f := newCapacityFixture(t, count)
			report := capacityReport{FixtureRows: count, Schema: f.schema, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, GOMAXPROCS: runtime.GOMAXPROCS(0), Started: begin.UTC().Format(time.RFC3339Nano), Workloads: map[string]any{}, Failures: []string{}}
			if err := f.api.DB.QueryRow(context.Background(), "SELECT version()").Scan(&report.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			if err := f.api.DB.QueryRow(context.Background(), `SELECT jsonb_object_agg(name,setting) FROM pg_settings WHERE name=ANY($1)`, []string{"shared_buffers", "work_mem", "effective_cache_size", "max_wal_size", "min_wal_size", "checkpoint_timeout", "fsync", "synchronous_commit", "full_page_writes", "jit", "random_page_cost", "max_connections"}).Scan(&report.PostgreSQLSettings); err != nil {
				t.Fatal(err)
			}
			report.DatabaseBytes = capacityCount(t, f.api.DB, `SELECT COALESCE(sum(pg_total_relation_size(c.oid)),0) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind='r'`)
			report.Queries = capacityPlans(t, f, repeats)
			for _, workload := range []struct {
				name string
				run  func(*testing.T, capacityFixture) map[string]any
			}{{"poison_deletion", capacityPoisonDeletes}, {"short_reconciliation", capacityShortReconciliation}, {"progressive_bootstrap", capacityBootstrap}, {"concurrent_admission_publication", capacityConcurrent}, {"legacy_null_deadline_admission", capacityLegacyNullDeadlines}, {"bounded_cache_backlog", capacityCacheBacklog}} {
				result := workload.run(t, f)
				report.Workloads[workload.name] = result
				if result["pass"] != true {
					report.Failures = append(report.Failures, workload.name)
				}
			}
			report.FinalDatabaseBytes = capacityCount(t, f.api.DB, `SELECT COALESCE(sum(pg_total_relation_size(c.oid)),0) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind='r'`)
			report.Memory = stopMemory()
			report.DurationMS = float64(time.Since(begin).Microseconds()) / 1000
			payload, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(output, "rows-"+raw+".json"), append(payload, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			if len(report.Failures) != 0 {
				t.Errorf("capacity regressions: %v (raw evidence retained)", report.Failures)
			}
		})
	}
}
