//go:build storagecapacity

package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageScanRepeatedFiveMillisecondCyclesMakeDurableProgress(t *testing.T) {
	f := newCapacityFixture(t, 1000)
	// Reuse the original capacity oracles unchanged: 32 actual 5ms contexts,
	// reconciliation progress after cycle8 of at least512 further entries (or
	// completion), followed by independent full-scan accounting/xmin checks.
	reconciliation := capacityShortReconciliation(t, f)
	t.Logf("reconciliation short-cycle registrations: %v", reconciliation["registered_after_each_cycle"])
	if reconciliation["pass"] != true {
		t.Error("original short reconciliation progress/accounting oracle failed")
	}
	// The same retained fixture then exercises real ReserveSource/bootstrap.
	// Its unbounded final recovery cannot turn failed short cycles into PASS.
	bootstrap := capacityBootstrap(t, f)
	t.Logf("bootstrap short-cycle registrations: %v", bootstrap["registered_after_each_cycle"])
	if bootstrap["pass"] != true {
		t.Error("original progressive bootstrap/initialization oracle failed")
	}
}

// This is a separate operational observation, not a substitute for the original
// 32x5ms capacity gate above. Fixture setup, reopening and read-back oracles are
// outside the timed product calls. No full/retry scan repairs either result.
func TestStorageScanOperationalBudgets1000(t *testing.T) {
	f := newCapacityFixture(t, 1000)
	const smallFiles = 1024
	const expectedFiles = 1000 + smallFiles
	const healthyArtifactContent = "healthy-artifact"
	expectedScanPaths := make([]string, 0, expectedFiles)
	expectedScanSizes := make([]int64, 0, expectedFiles)
	for i := 1; i <= f.retained; i++ {
		// The fixture deliberately expires i%100==1. Only the other paths
		// are physical preservation controls; each has its recorded16 bytes.
		if i%100 == 1 {
			continue
		}
		path := filepath.Join(f.c.DataDir, "artifacts", fmt.Sprintf("retained-%d", i))
		if err := os.WriteFile(path, []byte(healthyArtifactContent), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < expectedFiles; i++ {
		var content []byte
		if i < smallFiles {
			content = []byte("synthetic")
		}
		path := filepath.Join(f.c.DataDir, "artifacts", fmt.Sprintf("scan-%06d", i))
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		expectedScanPaths = append(expectedScanPaths, path)
		expectedScanSizes = append(expectedScanSizes, int64(len(content)))
	}
	type protectedEvidence struct {
		snapshot                                    string
		sources, jobs, artifacts, previews, badPins int64
		physicalFiles                               bool
	}
	protected := func(store *Store) protectedEvidence {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result := protectedEvidence{physicalFiles: true}
		err := store.DB.QueryRow(ctx, `SELECT jsonb_build_object(
	 'sources',(SELECT jsonb_agg((to_jsonb(s)-ARRAY['retention_job_pins','retention_preview_pins','retention_pins_valid','retention_pin_revision']) ORDER BY id) FROM sources s WHERE id LIKE 'retained-source-%'),
	 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j WHERE id LIKE 'retained-job-%'),
	 'healthy_artifacts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM artifacts a WHERE id LIKE 'retained-artifact-%' AND substring(id from '[0-9]+$')::integer%100<>1),
	 'preview_reservations',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM storage_reservations r WHERE id LIKE 'preview-reservation-%'))::text,
	 (SELECT count(*) FROM sources WHERE id LIKE 'retained-source-%'),
	 (SELECT count(*) FROM jobs WHERE id LIKE 'retained-job-%'),
	 (SELECT count(*) FROM artifacts WHERE id LIKE 'retained-artifact-%' AND substring(id from '[0-9]+$')::integer%100<>1),
	 (SELECT count(*) FROM storage_reservations WHERE id LIKE 'preview-reservation-%'),
	 (SELECT count(*) FROM sources s WHERE id LIKE 'retained-source-%'
	  AND (NOT retention_pins_valid OR retention_job_pins<>(SELECT count(*) FROM jobs j WHERE j.source_id=s.id)
	       OR retention_preview_pins<>(SELECT count(*) FROM storage_reservations r WHERE r.kind='preview' AND r.job_id=s.id)))`).Scan(&result.snapshot, &result.sources, &result.jobs, &result.artifacts, &result.previews, &result.badPins)
		if err != nil {
			t.Fatal("operational retained-row and pin oracle failed", err)
		}
		for i := 1; i <= f.retained; i++ {
			if i%100 == 1 {
				continue
			}
			if ctx.Err() != nil {
				t.Fatal("operational protected-file oracle exceeded its separate budget")
			}
			path := filepath.Join(f.c.DataDir, "artifacts", fmt.Sprintf("retained-%d", i))
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				result.physicalFiles = false
				continue
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != healthyArtifactContent {
				result.physicalFiles = false
			}
		}
		return result
	}
	type scanFileEvidence struct {
		badLedgerRows int64
		physicalFiles bool
	}
	scanFiles := func(store *Store) scanFileEvidence {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result := scanFileEvidence{physicalFiles: true}
		if err := store.DB.QueryRow(ctx, `SELECT count(*) FROM unnest($1::text[],$2::bigint[]) AS expected(path,size_bytes)
	 LEFT JOIN storage_files f ON f.path=expected.path
	 WHERE f.path IS NULL OR f.delete_pending OR f.size_bytes<>expected.size_bytes`, expectedScanPaths, expectedScanSizes).Scan(&result.badLedgerRows); err != nil {
			t.Fatal("operational per-path scan charge oracle failed", err)
		}
		for i, path := range expectedScanPaths {
			if ctx.Err() != nil {
				t.Fatal("operational scan-file oracle exceeded its separate budget")
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				result.physicalFiles = false
				continue
			}
			content, err := os.ReadFile(path)
			expected := ""
			if i < smallFiles {
				expected = "synthetic"
			}
			if err != nil || string(content) != expected {
				result.physicalFiles = false
			}
		}
		return result
	}
	protectedBefore := protected(f.api)
	if protectedBefore.sources != 1000 || protectedBefore.jobs != 1000 || protectedBefore.artifacts != 990 || protectedBefore.previews != 200 || protectedBefore.badPins != 0 || !protectedBefore.physicalFiles {
		t.Fatal("operational protected fixture is incomplete")
	}
	// newCapacityFixture seeds steady-state admission. Clear only this fresh,
	// owned namespace's observation marker so the timed call must bootstrap.
	capacityExec(t, f.api.DB, "DELETE FROM storage_state WHERE id=$1", f.c.DataDir)
	if apiDatabaseTimeout != 5*time.Second || workerMaintenanceTimeout != 10*time.Second {
		t.Fatal("operational experiment requires the recorded product budgets")
	}
	maintenanceCtx, stopMaintenance := context.WithTimeout(context.Background(), workerMaintenanceTimeout)
	maintenanceBegin := time.Now()
	maintenanceErr := RunMaintenance(maintenanceCtx, f.c, f.maintenance)
	maintenanceMS := float64(time.Since(maintenanceBegin).Microseconds()) / 1000
	maintenanceParentAlive := maintenanceCtx.Err() == nil
	stopMaintenance()

	// Reopen from the same owned namespace. Observing completed generations and
	// exact charges here requires durable state, not an in-memory reader ACK.
	readCtx, stopRead := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopRead()
	reopened, err := OpenStore(readCtx, f.api.DB.Config().ConnString())
	if err != nil {
		t.Fatal("operational experiment could not reopen its owned namespace", err)
	}
	t.Cleanup(reopened.DB.Close)
	reopened.ConfigureStorage(f.c)
	protectedAfterMaintenance := protected(reopened)
	scanAfterMaintenance := scanFiles(reopened)
	var initialized, registered, scanBytes, completedKinds int64
	err = reopened.DB.QueryRow(readCtx, `SELECT
	 (SELECT count(*) FROM storage_state WHERE id=$1),
	 (SELECT count(*) FROM storage_files WHERE path=ANY($2) AND NOT delete_pending),
	 (SELECT coalesce(sum(size_bytes),0)::bigint FROM storage_files WHERE path=ANY($2) AND NOT delete_pending),
	 (SELECT count(DISTINCT scan_kind) FROM storage_scan_progress
	  WHERE data_dir=$1 AND scan_kind=ANY($3) AND completed_at IS NOT NULL)`,
		f.c.DataDir, expectedScanPaths, append(append([]string{}, bootstrapScanKinds...), reconcileScanKinds...)).Scan(&initialized, &registered, &scanBytes, &completedKinds)
	if err != nil {
		t.Fatal("operational durable progress oracle failed", err)
	}
	maintenancePass := maintenanceErr == nil && maintenanceParentAlive && initialized == 1 && registered == expectedFiles && scanBytes == smallFiles*9 && completedKinds == 5 && scanAfterMaintenance.badLedgerRows == 0 && scanAfterMaintenance.physicalFiles

	// Make API bootstrap a separate fresh observation, outside its timed call.
	// Reset only this namespace's bootstrap cursors/marker and scan-file ledger;
	// keep the actual files, application rows and authoritative references.
	bootstrapAndSchedule := append([]string{}, bootstrapScanKinds...)
	for _, kind := range bootstrapScanKinds {
		bootstrapAndSchedule = append(bootstrapAndSchedule, "schedule_"+kind)
	}
	capacityExec(t, reopened.DB, "DELETE FROM storage_state WHERE id=$1", f.c.DataDir)
	capacityExec(t, reopened.DB, "DELETE FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=ANY($2)", f.c.DataDir, bootstrapAndSchedule)
	capacityExec(t, reopened.DB, "DELETE FROM storage_files WHERE path=ANY($1)", expectedScanPaths)
	var apiSetupMarkers, apiSetupFiles, apiSetupCursors int64
	if err = f.api.DB.QueryRow(readCtx, `SELECT
	 (SELECT count(*) FROM storage_state WHERE id=$1),
	 (SELECT count(*) FROM storage_files WHERE path=ANY($2)),
	 (SELECT count(*) FROM storage_scan_progress WHERE data_dir=$1 AND scan_kind=ANY($3))`, f.c.DataDir, expectedScanPaths, bootstrapAndSchedule).Scan(&apiSetupMarkers, &apiSetupFiles, &apiSetupCursors); err != nil {
		t.Fatal("operational API fresh-bootstrap setup oracle failed", err)
	}
	const owner, token = "operational-owner", "operational-token"
	var effectiveAPIBudget time.Duration
	apiBegin := time.Now()
	admissionErr := apiDatabaseExec(context.Background(), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return fmt.Errorf("API operation has no deadline")
		}
		effectiveAPIBudget = time.Until(deadline)
		return reopened.ReserveSource(ctx, f.c, owner, token)
	})
	apiMS := float64(time.Since(apiBegin).Microseconds()) / 1000
	var acknowledgedReservation, apiInitialized, apiRegistered, apiScanBytes, apiCompletedKinds int64
	if err = f.api.DB.QueryRow(readCtx, `SELECT
	 (SELECT count(*) FROM storage_reservations WHERE id=$1 AND owner=$2 AND token=$3 AND size_bytes=$4),
	 (SELECT count(*) FROM storage_state WHERE id=$5),
	 (SELECT count(*) FROM storage_files WHERE path=ANY($6) AND NOT delete_pending),
	 (SELECT coalesce(sum(size_bytes),0)::bigint FROM storage_files WHERE path=ANY($6) AND NOT delete_pending),
	 (SELECT count(DISTINCT scan_kind) FROM storage_scan_progress
	  WHERE data_dir=$5 AND scan_kind=ANY($7) AND completed_at IS NOT NULL)`, "source:"+owner, owner, token, f.c.MaxSourceBytes, f.c.DataDir, expectedScanPaths, bootstrapScanKinds).Scan(&acknowledgedReservation, &apiInitialized, &apiRegistered, &apiScanBytes, &apiCompletedKinds); err != nil {
		t.Fatal("operational reservation ACK oracle failed", err)
	}
	releaseErr := apiDatabaseExec(context.Background(), func(ctx context.Context) error {
		return reopened.ReleaseSource(ctx, owner, token)
	})
	var stored, files, reserved, reservations, remainingOwned int64
	if err = reopened.DB.QueryRow(readCtx, `SELECT stored_bytes,reserved_bytes,
	 (SELECT coalesce(sum(size_bytes),0)::bigint FROM storage_files),
	 (SELECT coalesce(sum(size_bytes),0)::bigint FROM storage_reservations),
	 (SELECT count(*) FROM storage_reservations WHERE id=$1)
	 FROM storage_counters WHERE id=1`, "source:"+owner).Scan(&stored, &reserved, &files, &reservations, &remainingOwned); err != nil {
		t.Fatal("operational full-accounting oracle failed", err)
	}
	scanAfterAPI := scanFiles(f.api)
	apiPass := apiSetupMarkers == 0 && apiSetupFiles == 0 && apiSetupCursors == 0 && admissionErr == nil && releaseErr == nil && effectiveAPIBudget > 0 && effectiveAPIBudget <= apiDatabaseTimeout && acknowledgedReservation == 1 && remainingOwned == 0 && apiInitialized == 1 && apiRegistered == expectedFiles && apiScanBytes == smallFiles*9 && apiCompletedKinds == 3 && scanAfterAPI.badLedgerRows == 0 && scanAfterAPI.physicalFiles
	accountingPass := stored == files && reserved == reservations
	protectedAfterAPI := protected(f.api)
	protectedPass := protectedBefore.snapshot == protectedAfterMaintenance.snapshot && protectedBefore.snapshot == protectedAfterAPI.snapshot && protectedAfterMaintenance.sources == 1000 && protectedAfterAPI.sources == 1000 && protectedAfterMaintenance.jobs == 1000 && protectedAfterAPI.jobs == 1000 && protectedAfterMaintenance.artifacts == 990 && protectedAfterAPI.artifacts == 990 && protectedAfterMaintenance.previews == 200 && protectedAfterAPI.previews == 200 && protectedAfterMaintenance.badPins == 0 && protectedAfterAPI.badPins == 0 && protectedAfterMaintenance.physicalFiles && protectedAfterAPI.physicalFiles
	report := map[string]any{
		"experiment": "storage_scan_operational_budgets_1000", "initial_retained_rows": f.retained,
		"scan_physical_files": expectedFiles, "protected_physical_files": 990, "total_physical_files": expectedFiles + 990,
		"fixture": "fresh_namespace_prepared_pools_run_maintenance_then_api", "maintenance_entrypoint": "RunMaintenance_one_shot_not_worker_loop",
		"run_maintenance_budget_ms": workerMaintenanceTimeout.Milliseconds(), "run_maintenance_ms": maintenanceMS,
		"run_maintenance_acknowledged": maintenanceErr == nil, "run_maintenance_parent_alive_after_return": maintenanceParentAlive,
		"initialization_markers_after_reopen": initialized, "registered_files_after_reopen": registered,
		"registered_scan_bytes_after_reopen": scanBytes, "completed_scan_kinds_after_reopen": completedKinds,
		"run_maintenance_pass": maintenancePass, "api_budget_ms": apiDatabaseTimeout.Milliseconds(),
		"effective_api_budget_ms": float64(effectiveAPIBudget.Microseconds()) / 1000, "api_ms": apiMS,
		"api_setup_markers": apiSetupMarkers, "api_setup_observed_files": apiSetupFiles, "api_setup_bootstrap_cursors": apiSetupCursors,
		"api_initialization_markers": apiInitialized, "api_registered_files": apiRegistered,
		"api_registered_scan_bytes": apiScanBytes, "api_completed_bootstrap_kinds": apiCompletedKinds,
		"reservation_acknowledged": acknowledgedReservation == 1, "api_pass": apiPass,
		"stored_bytes": stored, "file_ledger_bytes": files, "reserved_bytes": reserved, "reservation_ledger_bytes": reservations,
		"remaining_owned_reservations": remainingOwned, "accounting_pass": accountingPass,
		"protected_rows_before_sha256":            fmt.Sprintf("%x", sha256.Sum256([]byte(protectedBefore.snapshot))),
		"protected_rows_after_maintenance_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(protectedAfterMaintenance.snapshot))),
		"protected_rows_after_api_sha256":         fmt.Sprintf("%x", sha256.Sum256([]byte(protectedAfterAPI.snapshot))),
		"retained_sources_after_maintenance":      protectedAfterMaintenance.sources, "retained_sources_after_api": protectedAfterAPI.sources,
		"retained_jobs_after_maintenance": protectedAfterMaintenance.jobs, "retained_jobs_after_api": protectedAfterAPI.jobs,
		"healthy_artifacts_after_maintenance": protectedAfterMaintenance.artifacts, "healthy_artifacts_after_api": protectedAfterAPI.artifacts,
		"preview_pins_after_maintenance": protectedAfterMaintenance.previews, "preview_pins_after_api": protectedAfterAPI.previews,
		"healthy_physical_files_after_maintenance": protectedAfterMaintenance.physicalFiles, "healthy_physical_files_after_api": protectedAfterAPI.physicalFiles,
		"bad_scan_ledger_rows_after_maintenance": scanAfterMaintenance.badLedgerRows, "bad_scan_ledger_rows_after_api": scanAfterAPI.badLedgerRows,
		"scan_physical_files_intact_after_maintenance": scanAfterMaintenance.physicalFiles, "scan_physical_files_intact_after_api": scanAfterAPI.physicalFiles,
		"protected_pass":    protectedPass,
		"original_5ms_gate": "separate_unchanged_test_result_required", "replaces_original_5ms_result": false,
		"operational_pass": maintenancePass && apiPass && accountingPass && protectedPass,
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("operational_budget_evidence=%s", encoded)
	if !maintenancePass || !apiPass || !accountingPass || !protectedPass {
		t.Error("operational budget, durable progress, reservation ACK or accounting oracle failed")
	}
}
