package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func storageProgressRow(t *testing.T, s *Store, c Config, kind, path string) storageScanProgress {
	t.Helper()
	var p storageScanProgress
	err := s.DB.QueryRow(context.Background(), `SELECT scan_kind,path,position,prefix_digest,generation_mtime_ns,generation_size,started_at FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=$2 AND path=$3`, c.DataDir, kind, path).Scan(&p.kind, &p.path, &p.position, &p.digest, &p.mtime, &p.size, &p.started)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func storageProgressRestart(t *testing.T, s *Store) *Store {
	t.Helper()
	db, err := pgxpool.NewWithConfig(context.Background(), s.DB.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return &Store{DB: db}
}

func TestStorageBootstrapProgressSurvivesRestart(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	const workspaces = storageProgressBatches + 8
	for i := range storageRegistrationBatch + 3 {
		writeLedgerFile(t, c, "sources", fmt.Sprintf("source-%04d", i), 1)
	}
	writeLedgerFile(t, c, "artifacts", "artifact", 7)
	for i := range workspaces {
		writeLedgerFile(t, c, "work", fmt.Sprintf("legacy-%03d/partial", i), 3)
	}
	start := time.Now()
	if err := s.ReserveSource(ctx, c, "new", "token"); !errors.Is(err, ErrStorageInitializing) {
		t.Fatal("incomplete accounting did not fail closed", err)
	}
	stored, reserved := ledgerBytes(t, s)
	if stored <= 0 || reserved != 0 {
		t.Fatal("partial observations rolled back or admission leaked a reservation", stored, reserved)
	}
	var initialized bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_state WHERE id=$1)`, c.DataDir).Scan(&initialized); err != nil || initialized {
		t.Fatal("premature initialized marker", initialized, err)
	}
	// A root completed before the nested work backlog. New membership must be
	// revalidated before opening admission, including after another pool starts.
	writeLedgerFile(t, c, "sources", "late-before-ready", 11)
	restarted := storageProgressRestart(t, s)
	for attempts := 0; ; attempts++ {
		err := restarted.ReserveSource(ctx, c, "new", "token")
		if err == nil {
			break
		}
		if !errors.Is(err, ErrStorageInitializing) || attempts > 10 {
			t.Fatal("restarted bootstrap failed to progress", err)
		}
	}
	stored, reserved = ledgerBytes(t, s)
	want := int64(storageRegistrationBatch + 3 + 7 + workspaces*3 + 11)
	if stored != want || reserved != c.MaxSourceBytes {
		t.Fatal("bootstrap lost files, double counted replay, or admitted without full accounting", stored, want, reserved)
	}
	t.Logf("bootstrap partial=%d final=%d bytes, restart+membership verification elapsed=%s", stored-11, stored, time.Since(start))
}

func TestStorageScanCanceledBatchDoesNotAdvance(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	writeLedgerFile(t, c, "sources", "retained", 13)
	ctx := context.Background()
	p, err := s.nextStorageScan(ctx, c, []string{"bootstrap_sources"}, false)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.storageTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	bounded, cancel := context.WithTimeout(ctx, 35*time.Millisecond)
	start := time.Now()
	err = s.storageScanStep(bounded, c, p)
	cancel()
	rollbackStorage(lock)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked batch did not honor its deadline", err)
	}
	after := storageProgressRow(t, s, c, p.kind, p.path)
	if after.position != 0 || after.digest != "" {
		t.Fatal("canceled batch advanced durable coverage", after)
	}
	if stored, _ := ledgerBytes(t, s); stored != 0 {
		t.Fatal("canceled batch partially committed accounting", stored)
	}
	restarted := storageProgressRestart(t, s)
	if err = restarted.storageScanStep(ctx, c, after); err != nil {
		t.Fatal("rollback retained a lock or blocked recovery", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 13 {
		t.Fatal("retry lost observation", stored)
	}
	t.Logf("canceled lock wait and recovered observation elapsed=%s", time.Since(start))
}

func TestStorageScanPrefixChangeRestartsCoverage(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	for i := range storageRegistrationBatch + 7 {
		writeLedgerFile(t, c, "sources", fmt.Sprintf("file-%04d", i), 1)
	}
	p, err := s.nextStorageScan(ctx, c, []string{"reconcile_sources"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.storageScanStep(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	p = storageProgressRow(t, s, c, p.kind, p.path)
	if p.position != storageRegistrationBatch {
		t.Fatal("fixture did not stop at a bounded batch", p.position)
	}
	dir, err := os.Open(p.path)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := dir.ReadDir(1)
	dir.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(p.path, prefix[0].Name()), filepath.Join(p.path, "renamed-prefix")); err != nil {
		t.Fatal(err)
	}
	// Match the new fingerprint deliberately: the independently checked prefix
	// digest must catch reordering even when metadata alone cannot distinguish it.
	info, err := os.Stat(p.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE storage_scan_progress SET generation_mtime_ns=$1,generation_size=$2 WHERE data_dir=$3 AND path=$4`, info.ModTime().UnixNano(), info.Size(), c.DataDir, p.path); err != nil {
		t.Fatal(err)
	}
	p = storageProgressRow(t, s, c, p.kind, p.path)
	restarted := storageProgressRestart(t, s)
	if err = restarted.storageScanStep(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	p = storageProgressRow(t, s, c, p.kind, p.path)
	if p.position != 0 || p.digest != "" {
		t.Fatal("changed prefix was incorrectly accepted as coverage", p.position)
	}
	for range 3 {
		if err = restarted.storageScanStep(ctx, c, p); err != nil {
			t.Fatal(err)
		}
		p = storageProgressRow(t, s, c, p.kind, p.path)
	}
	var covered int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM storage_files WHERE starts_with(path,$1)`, filepath.Join(c.DataDir, "sources")+string(os.PathSeparator)).Scan(&covered); err != nil || covered < storageRegistrationBatch+7 {
		t.Fatal("reset skipped current tail entries", covered, err)
	}
}

func TestStorageReconcileCoverageGatesExpiredReservations(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	for i := range storageRegistrationBatch + 5 {
		writeLedgerFile(t, c, "sources", fmt.Sprintf("orphan-%04d", i), 1)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at) VALUES('old','old','source',100,clock_timestamp()-interval '1 minute')`); err != nil {
		t.Fatal(err)
	}
	artifactPass, err := s.nextStorageScan(ctx, c, []string{"reconcile_artifacts"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.storageScanStep(ctx, c, artifactPass); err != nil {
		t.Fatal(err)
	}
	p, err := s.nextStorageScan(ctx, c, []string{"reconcile_sources"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.storageScanStep(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	p = storageProgressRow(t, s, c, p.kind, p.path)
	if _, err = s.DB.Exec(ctx, `INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at) VALUES('during-pass','during-pass','source',200,clock_timestamp()-interval '1 microsecond')`); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileStorageProgress(ctx, c, 0); err != nil {
		t.Fatal(err)
	}
	if _, reserved := ledgerBytes(t, s); reserved != 300 {
		t.Fatal("partial scan released an expired preparation", reserved)
	}
	if err = s.storageScanStep(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileStorageProgress(ctx, c, 0); err != nil {
		t.Fatal(err)
	}
	if _, reserved := ledgerBytes(t, s); reserved != 200 {
		t.Fatal("coverage released a preparation that expired during the pass", reserved)
	}
	if err = s.ReconcileStorage(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, reserved := ledgerBytes(t, s); reserved != 0 {
		t.Fatal("next completed generation failed to release covered preparation", reserved)
	}
}

func TestStorageWorkProgressSurvivesRestartAndUnsafeCandidate(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	good := writeLedgerFile(t, c, "work", "legacy/partial", 9)
	root := filepath.Dir(good)
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(root, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes) VALUES($1,'work_orphan',9);`, good); err != nil {
		t.Fatal(err)
	}
	// More unsafe candidates than one attempt batch. Their queue entries remain
	// charged/retryable, but scheduling must reach the valid tail after restart.
	if _, err := s.DB.Exec(ctx, `INSERT INTO storage_work_cleanup(data_dir,path) SELECT $1,$2||'/../unsafe-'||i FROM generate_series(1,$3) i`, c.DataDir, filepath.Join(c.DataDir, "work"), storageWorkAttempts+5); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupWorkProgress(ctx, c); err == nil {
		t.Fatal("unsafe paths were silently accepted")
	}
	restarted := storageProgressRestart(t, s)
	_ = restarted.cleanupWorkProgress(ctx, c)
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("unsafe head blocked valid workspace after restart", err)
	}
	if stored, _ := ledgerBytes(t, s); stored != 0 {
		t.Fatal("durably removed legacy workspace remained charged", stored)
	}
}

func TestStorageScanRejectsSymlinkedRoot(t *testing.T) {
	c := ledgerConfig(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(c.DataDir, "sources")); err != nil {
		t.Skip("symlink creation unsupported", err)
	}
	_, _, _, _, _, err := readStorageScanBatch(context.Background(), c, storageScanProgress{kind: "bootstrap_sources", path: filepath.Join(c.DataDir, "sources")})
	if err == nil {
		t.Fatal("scan traversed a symlink root")
	}
}

func TestStorageScanClassesRemainFairAcrossNewWorkAndRestart(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	for _, kind := range []string{"sources", "artifacts"} {
		for i := range storageRegistrationBatch*2 + 3 {
			writeLedgerFile(t, c, kind, fmt.Sprintf("file-%04d", i), 1)
		}
	}
	const descendants = 70
	for i := range descendants {
		writeLedgerFile(t, c, "work", fmt.Sprintf("workspace-%03d/partial", i), 1)
	}
	counts := map[string]int{}
	for attempt := range 6 {
		if attempt == 3 {
			s = storageProgressRestart(t, s)
		}
		p, err := s.nextStorageScan(ctx, c, bootstrapScanKinds, false)
		if err != nil {
			t.Fatal(err)
		}
		counts[p.kind]++
		if err = s.storageScanStep(ctx, c, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range bootstrapScanKinds {
		if counts[kind] != 2 {
			t.Fatal("new work descendants displaced another scan class", counts)
		}
	}
	for _, kind := range []string{"bootstrap_sources", "bootstrap_artifacts"} {
		p := storageProgressRow(t, s, c, kind, scanRoot(c, kind))
		if p.position != storageRegistrationBatch*2 {
			t.Fatal("class attempts did not preserve committed progress after restart", kind, p.position)
		}
	}
	var pending int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind='bootstrap_work' AND completed_at IS NULL`, c.DataDir).Scan(&pending); err != nil || pending != descendants-1 {
		t.Fatal("fixture did not leave an unvisited work backlog", pending, err)
	}
	var incorrectlyVerified int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind='bootstrap_work' AND completed_at IS NOT NULL AND last_visited_at>=completed_at`, c.DataDir).Scan(&incorrectlyVerified); err != nil || incorrectlyVerified != 0 {
		t.Fatal("class scheduling silently verified directory completion", incorrectlyVerified, err)
	}
	t.Logf("first six committed attempts across restart=%v, remaining work descendants=%d", counts, pending)
}

func TestStorageBootstrapAmortizesLegacyExpiryAndResumesAfterCancellation(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	ctx := context.Background()
	path := writeLedgerFile(t, c, "sources", "legacy.media", 7)
	if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,path) VALUES('legacy-source','legacy','Legacy',1000,'upload',$1)`, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO jobs(id,owner,source_id,request,items,status) VALUES('legacy-job','legacy','legacy-source','{}','[]','succeeded')`); err != nil {
		t.Fatal(err)
	}
	const legacyRows = storageProgressBatches*storageBatch + 201
	if _, err := s.DB.Exec(ctx, `INSERT INTO artifacts(id,owner,job_id,path,filename,size_bytes,actual_start_ms,actual_end_ms,created_at) SELECT 'legacy-'||i,'legacy','legacy-job','','legacy.mp4',0,0,1000,clock_timestamp()-interval '2 hours' FROM generate_series(1,$1) i`, legacyRows); err != nil {
		t.Fatal(err)
	}
	updated := func() int {
		t.Helper()
		var count int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE expires_at IS NOT NULL`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	assertNotReady := func() {
		t.Helper()
		var ready bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_state WHERE id=$1)`, c.DataDir).Scan(&ready); err != nil || ready {
			t.Fatal("backfill marked incomplete accounting ready", ready, err)
		}
	}
	start := time.Now()
	if err := s.bootstrapStorage(ctx, c); !errors.Is(err, ErrStorageInitializing) {
		t.Fatal("large backfill did not respect its independent batch budget", err)
	}
	first := updated()
	if first <= storageBatch || first > storageProgressBatches*storageBatch || first >= legacyRows {
		t.Fatal("backfill failed to amortize, exceeded its bound, or lost the long fixture", first)
	}
	assertNotReady()
	// Membership can change between expiry batches/calls, after the source pass
	// already finished. The final generation check must adopt this file too.
	writeLedgerFile(t, c, "sources", "late-during-backfill", 11)
	lock, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStorage(lock)
	if _, err = lock.Exec(ctx, `LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	err = s.bootstrapStorage(bounded, c)
	cancel()
	rollbackStorage(lock)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked backfill failed to honor cancellation", err)
	}
	if got := updated(); got != first {
		t.Fatal("cancellation rolled back committed batches or advanced blocked batch", got, first)
	}
	assertNotReady()
	restarted := storageProgressRestart(t, s)
	if err = restarted.bootstrapStorage(ctx, c); err != nil {
		t.Fatal("new pool failed to resume backfill or retained the canceled lock", err)
	}
	if got := updated(); got != legacyRows {
		t.Fatal("restart omitted legacy expiry rows", got)
	}
	var incorrect int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE expires_at<>created_at+($1*interval '1 second')`, c.ArtifactTTL.Seconds()).Scan(&incorrect); err != nil || incorrect != 0 {
		t.Fatal("backfill changed expiry semantics", incorrect, err)
	}
	if stored, reserved := ledgerBytes(t, s); stored != 18 || reserved != 0 {
		t.Fatal("final marker lost generation changes or created a reservation", stored, reserved)
	}
	var ready bool
	if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_state WHERE id=$1)`, c.DataDir).Scan(&ready); err != nil || !ready {
		t.Fatal("completed backfill did not open admission", ready, err)
	}
	t.Logf("legacy expiry rows=%d first-call committed=%d, blocked cancellation+restart elapsed=%s", legacyRows, first, time.Since(start))
}
