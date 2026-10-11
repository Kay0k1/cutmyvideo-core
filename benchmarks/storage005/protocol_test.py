"""Synthetic protocol controls. No native/Go/database/media work is permitted."""
import copy
import importlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock
import urllib.request

ACTUAL_ATTEMPTS = 0


def forbidden(*args, **kwargs):
    global ACTUAL_ATTEMPTS
    ACTUAL_ATTEMPTS += 1
    raise AssertionError("actual child/network operation forbidden in pure controls")


# Install before any candidate helper import, including module-level code.
GUARDS = [mock.patch.object(subprocess, "Popen", forbidden), mock.patch.object(urllib.request, "urlopen", forbidden), mock.patch.object(os, "system", forbidden)]
for process_name in ("posix_spawn", "posix_spawnp"):
    if hasattr(os, process_name):
        GUARDS.append(mock.patch.object(os, process_name, forbidden))
for global_guard in GUARDS:
    global_guard.start()

from protocol import (DML_SQL, PG_CONFIG, PG_INDEX, PG_CHILD, PG_SETTINGS, Refused,
                      canonical, loads, sha, validate_go_json, validate_identity,
                      validate_image, validate_report)
from verify_saved import verify_catalog

IDENTITY = loads(Path(__file__).with_name("identity.json").read_bytes())


def latency(count):
    return {"samples_ms": [1.0] * count, "p50_ms": 1.0, "p95_ms": 1.0, "max_ms": 1.0}


def query(name, sql):
    return {"name": name, "sql": sql, "client_round_trip": latency(20), "postgres_execution": latency(20), "explain_analyze_buffers": [[{"Plan": {}, "Execution Time": 1.0}]] * 20}


def go_events(action):
    package = "github.com/Kay0k1/cutmyvideo-core/internal/app"
    return b"".join((json.dumps(event) + "\n").encode() for event in ({"Package": package, "Test": "TestStorage005ComparisonPilot", "Action": "run"}, {"Package": package, "Test": "TestStorage005ComparisonPilot", "Action": action}, {"Package": package, "Action": action}))


def fixture(label="after", size=1000, short_pass=True):
    version = IDENTITY["versions"][label]
    initial = {table: {"rows": count, "digest": "0" * 32} for table, count in (("sources", size + 200), ("jobs", size + 200), ("reservations", 200), ("files", size + 200 + max(200, size // 20)))}
    initial["totals"] = [{"id": 1, "stored_bytes": 16000, "reserved_bytes": 3200}]
    progress = [size + 1024] * 32 if short_pass else [0] * 32
    ttl = {"pass": True, "samples": 20, "before": initial, "after": copy.deepcopy(initial), "rollback_after_each_sample": True, "sample_rows": [{"retained_pinned_remaining": size, "expired_remaining": 0, "eligible_deleted": 200}] * 20, "query": query("source_retention", version["selector_sql"]), "fence": latency(20)}
    dml = {"pass": True, "samples_per_operation": 20, "before": initial, "after": copy.deepcopy(initial), "queries": [query(name, sql) for name, sql in DML_SQL.items()]}
    held = {"pass": True, "attempts": [{"budget_ms": n, "wall_ms": float(n), "deadline": True, "unexpected_acquisition": False} for n in (5, 50)], "acquired_after_release": True, "wall_limit_claimed": False}
    audit = {"applicable": False, "reason": "schema004 has no source-pin audit", "pass": True}
    if label == "after":
        audit = {"applicable": True, "pass": True, "hot_job_count": 128, "hot_preview_count": 128, "healthy_successor_repaired": True, "complete_child_audit_claimed": False, "audit_only_fixture_removed": True, "calls": [{"unit": n, "elapsed_ms": 1.0, "done": False, "repaired_eligible": n == 2} for n in range(1, 11)], "progress": {"partial": [{"source_id": "comparison-a-hot", "job_count": 128, "preview_count": 128, "job_done": False, "preview_done": False}]}}
    foreground = {"pass": True, "admission_workers": 4, "operations_per_worker": 20, "errors": [], "published_sources": 80, "source_reservations_remaining": 0, "counters_match_actual_ledger": True}
    foreground.update({name: latency(count) for name, count in (("reserve_source", 80), ("durable_source_publication", 80), ("retention_batch", 20), ("persisted_metadata_hit", 80))})
    short = {"pass": short_pass, "cycles": 32, "cycle_budget_ms": 5, "latency": latency(32), "registered_after_each_cycle": progress, "physical_zero_byte_entries": size, "physical_small_files": 1024, "physical_files": size + 1024, "registered_before_full_scan": progress[-1], "unchanged_scan_preserves_tuple_xmin": True}
    bootstrap = {"pass": short_pass, "cycles": 32, "cycle_budget_ms": 5, "latency": latency(32), "registered_after_each_cycle": progress, "registered_before_final_pass": progress[-1], "initialization_markers": 1, "final_registered_files": size + 1024}
    return {"format": 1, "kind": "storage005-comparison-pilot", "size": size, "schema": version["schema"], "complete": True, "pass": short_pass, "sql_sha256": version["selector_sql_sha256"], "postgresql_settings": dict(PG_SETTINGS, server_version="17.11 (synthetic fixture)"), "runtime_role": {"pass": True, "superuser": False, "schema_create": False, "migration_ledger_insert": False, "ordinary_table_dml": True, "function_execute_granted": False, "session_login_is_fixture_admin": True}, "initial_rows": initial, "stages": {"source_ttl": ttl, "foreground_dml": dml, "held_fence": held, "persisted_audit": audit, "foreground_maintenance": foreground, "short_reconciliation": short, "progressive_bootstrap": bootstrap}, "failures": [] if short_pass else ["short_reconciliation", "progressive_bootstrap"]}


class PilotProtocolControls(unittest.TestCase):
    @classmethod
    def tearDownClass(cls):
        try:
            assert ACTUAL_ATTEMPTS == 0, ACTUAL_ATTEMPTS
            print("pure actual child/network attempts: 0", flush=True)
        finally:
            for guard in reversed(GUARDS):
                guard.stop()

    def rejected(self, value, label="after", size=1000):
        with self.assertRaises(Refused):
            validate_report(value, label, size, IDENTITY)

    def test_fixed_identity_and_both_pilot_pairs(self):
        validate_identity(IDENTITY)
        for label in ("before", "after"):
            for size in (1000, 10000):
                self.assertTrue(validate_report(fixture(label, size), label, size, IDENTITY)["workload_pass"])

    def test_runtime_and_workflow_imports_start_no_operations(self):
        importlib.import_module("run_hosted")
        importlib.import_module("verify_saved")
        self.assertEqual(ACTUAL_ATTEMPTS, 0)

    def test_changed_commit_parent_tree_inventory_or_pg_pin_refuse(self):
        for path, replacement in ((["versions", "before", "commit"], "0" * 40), (["versions", "after", "parents"], []), (["versions", "after", "tree"], "0" * 40), (["versions", "before", "application_inventory_sha256"], "0" * 64), (["postgres", "config_digest"], "sha256:" + "0" * 64)):
            v = copy.deepcopy(IDENTITY)
            target = v
            for key in path[:-1]:
                target = target[key]
            target[path[-1]] = replacement
            with self.assertRaises(Refused):
                validate_identity(v)

    def test_100k_and_unknown_schema_refuse(self):
        self.rejected(fixture(size=100000), size=100000)
        v = fixture(); v["schema"] = 4; self.rejected(v)

    def test_original_5ms_failures_remain_whole_workload_failures(self):
        v = fixture(short_pass=False)
        proof = validate_report(v, "after", 1000, IDENTITY)
        self.assertTrue(proof["verified_measurements"])
        self.assertTrue(proof["correctness_stages_pass"])
        self.assertFalse(proof["workload_pass"])
        self.assertEqual(proof["failed_stages"], ["progressive_bootstrap", "short_reconciliation"])
        v["pass"] = True; self.rejected(v)

    def test_later_full_scan_cannot_repair_zero_progress(self):
        for stage in ("short_reconciliation", "progressive_bootstrap"):
            v = fixture(short_pass=False); v["stages"][stage]["pass"] = True; self.rejected(v)

    def test_partial_report_or_missing_stage_refuses(self):
        v = fixture(); v["complete"] = False; self.rejected(v)
        v = fixture(); del v["stages"]["held_fence"]; self.rejected(v)

    def test_source_selector_sql_and_row_authority_refuse_drift(self):
        v = fixture(); v["stages"]["source_ttl"]["query"]["sql"] = "SELECT1"; self.rejected(v)
        v = fixture(); v["stages"]["source_ttl"]["sample_rows"] = [{"retained_pinned_remaining": 999, "expired_remaining": 0, "eligible_deleted": 200}] * 20; self.rejected(v)

    def test_mutated_rollback_membership_or_accounting_refuses(self):
        v = fixture(); v["stages"]["source_ttl"]["after"]["sources"]["digest"] = "1" * 32; self.rejected(v)
        v = fixture(); v["stages"]["foreground_dml"]["after"]["totals"][0]["stored_bytes"] += 1; self.rejected(v)

    def test_raw_plan_time_and_percentile_must_match(self):
        v = fixture(); v["stages"]["source_ttl"]["query"]["explain_analyze_buffers"][0] = [{"Plan": {}, "Execution Time": 2.0}]; self.rejected(v)
        v = fixture(); v["stages"]["source_ttl"]["query"]["client_round_trip"]["p95_ms"] = 2.0; self.rejected(v)

    def test_foreground_mutation_sql_or_reduced_samples_refuse(self):
        v = fixture(); v["stages"]["foreground_dml"]["queries"][0]["sql"] = "SELECT1"; self.rejected(v)
        v = fixture(); v["stages"]["foreground_dml"]["queries"][0]["explain_analyze_buffers"].pop(); self.rejected(v)

    def test_effective_superuser_or_ledger_write_refuses(self):
        for key in ("superuser", "schema_create", "migration_ledger_insert", "function_execute_granted"):
            v = fixture(); v["runtime_role"][key] = True; self.rejected(v)

    def test_unmatched_postgres_settings_refuse(self):
        v = fixture(); v["postgresql_settings"]["fsync"] = "off"; self.rejected(v)
        v = fixture(); v["postgresql_settings"]["server_version_num"] = "170012"; self.rejected(v)

    def test_audit_na_is_explicit_and_cannot_transfer_to005(self):
        v = fixture("before"); validate_report(v, "before", 1000, IDENTITY)
        v = fixture(); v["stages"]["persisted_audit"] = fixture("before")["stages"]["persisted_audit"]; self.rejected(v)

    def test_partial_hot_source_cannot_be_claimed_complete(self):
        for change in ("bound", "done", "successor", "full"):
            v = fixture(); a = v["stages"]["persisted_audit"]
            if change == "bound": a["hot_job_count"] = 129
            if change == "done": a["progress"]["partial"][0]["job_done"] = True
            if change == "successor": a["healthy_successor_repaired"] = False
            if change == "full": a["complete_child_audit_claimed"] = True
            self.rejected(v)

    def test_foreground_errors_cannot_be_hidden_by_publication_count(self):
        v = fixture(); v["stages"]["foreground_maintenance"]["errors"] = ["synthetic operation failed"]; self.rejected(v)

    def test_held_lock_and_recovery_false_success_refuse(self):
        v = fixture(); v["stages"]["held_fence"]["attempts"][0]["unexpected_acquisition"] = True; self.rejected(v)
        v = fixture(); v["stages"]["held_fence"]["acquired_after_release"] = False; self.rejected(v)

    def test_actual_go_terminal_must_agree_and_never_skip(self):
        validate_go_json(go_events("pass"), 0, True)
        validate_go_json(go_events("fail"), 1, False)
        for action, code, passed in (("skip", 0, True), ("pass", 1, True), ("pass", 0, False)):
            with self.assertRaises(Refused): validate_go_json(go_events(action), code, passed)

    def test_malformed_nested_diagnostic_shapes_refuse(self):
        for path in (("runtime_role",), ("stages", "held_fence", "attempts"), ("stages", "persisted_audit", "progress"), ("stages", "foreground_dml", "queries")):
            v = fixture(); target = v
            for key in path[:-1]: target = target[key]
            target[path[-1]] = None
            self.rejected(v)

    def test_duplicate_json_keys_and_nonfinite_numbers_refuse(self):
        for raw in ('{"pass":false,"pass":true}', '{"elapsed":NaN}'):
            with self.assertRaises(Refused): loads(raw)

    def test_pinned_registry_fixture_bytes_and_config(self):
        path = Path(__file__).with_name("fixtures")
        for name, digest in (("registry-index.raw.json", PG_INDEX), ("registry-amd64.raw.json", PG_CHILD)):
            self.assertEqual("sha256:" + sha((path / name).read_bytes()), digest)
        self.assertEqual(loads((path / "registry-amd64.raw.json").read_bytes())["config"]["digest"], PG_CONFIG)

    def test_image_index_identity_allowed_but_platform_limits_and_repo_strict(self):
        container = [PG_INDEX, "docker.io/library/postgres@" + PG_INDEX, "linux", 1073741824, 2000000000, 1073741824, False, True]
        image = [PG_INDEX, "linux", "amd64", ["postgres@" + PG_INDEX]]
        def raw(v): return b"".join((json.dumps(x) + "\n").encode() for x in v)
        self.assertEqual(validate_image(raw(container), raw(image))["config_digest"], PG_CONFIG)
        for field, value in ((2, "windows"), (3, 0), (5, -1), (6, True)):
            changed = copy.deepcopy(container); changed[field] = value
            with self.assertRaises(Refused): validate_image(raw(changed), raw(image))
        image[3] = ["foreign/repository@" + PG_INDEX]
        with self.assertRaises(Refused): validate_image(raw(container), raw(image))

    def catalog_fixture(self, root):
        (root / "summary.json").write_bytes(b"{}")
        (root / "original.raw").write_bytes(b"original")
        return {"original.raw": {"bytes": 8, "sha256": sha(b"original")}}

    def test_saved_catalog_accepts_only_exact_original_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); catalog = self.catalog_fixture(root)
            self.assertEqual(verify_catalog(root, catalog), 1)
            (root / "original.raw").write_bytes(b"mutation")
            with self.assertRaises(Refused): verify_catalog(root, catalog)

    def test_saved_catalog_missing_or_extra_file_refuses(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); catalog = self.catalog_fixture(root)
            (root / "extra").write_bytes(b"extra")
            with self.assertRaises(Refused): verify_catalog(root, catalog)
            (root / "extra").unlink(); (root / "original.raw").unlink()
            with self.assertRaises(Refused): verify_catalog(root, catalog)

    def test_saved_catalog_escape_and_symlink_refuse(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); catalog = self.catalog_fixture(root)
            for bad in ("../original.raw", "/original.raw", "./original.raw"):
                with self.assertRaises(Refused): verify_catalog(root, {bad: catalog["original.raw"]})
            (root / "link").symlink_to(root / "original.raw")
            with self.assertRaises(Refused): verify_catalog(root, catalog)

    def test_saved_catalog_size_and_bool_typed_counts_refuse(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); catalog = self.catalog_fixture(root)
            for size in (True, -1, 65 * 1024 * 1024):
                changed = copy.deepcopy(catalog); changed["original.raw"]["bytes"] = size
                with self.assertRaises(Refused): verify_catalog(root, changed)


if __name__ == "__main__":
    unittest.main()
