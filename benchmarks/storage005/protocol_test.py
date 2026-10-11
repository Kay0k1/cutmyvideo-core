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
                      admitted_sizes, canonical, loads, promotion_ready, sha, validate_go_json, validate_identity,
                      validate_image, validate_report)
from verify_saved import verify_catalog

IDENTITY = loads(Path(__file__).with_name("identity.json").read_bytes())

# Actual before-1000 foreground from run38123312708/attempt1, preserved report
# SHA027449077f935bb266536400852e7ac8cee26813bed581116745666deeb50b85.
# Only this subobject is copied; all measured values and explicit null remain.
ACTUAL_FOREGROUND_NULL = loads(b'{"admission_workers":4,"advisory_lock_samples":75,"counters_match_actual_ledger":true,"durable_source_publication":{"samples_ms":[32.87,30.405,28.358,26.47,9.677,9.225,8.78,8.229,10.319,9.806,9.302,8.879,12.414,11.878,10.564,9.815,10.006,9.159,8.329,7.655,11.036,9.96,9.457,8.404,8.959,8.17,7.356,6.995,9.414,8.544,8.101,7.262,9.417,9.082,8.23,7.263,9.318,8.464,8.165,7.572,8.624,8.348,7.617,7.001,7.27,6.3,5.907,5.568,7.682,6.875,5.936,5.036,6.347,5.905,5.457,5.133,6.977,6.783,5.908,5.591,7.806,7.033,6.258,5.309,7.308,6.541,5.823,5.383,8.106,7.187,6.317,5.428,7.435,6.502,7.097,6.16,7.084,6.507,5.43,4.901],"p50_ms":7.617,"p95_ms":12.414,"max_ms":32.87},"duration_ms":383.008,"errors":null,"max_observed_waiters":4,"operations_per_worker":20,"pass":true,"persisted_metadata_hit":{"samples_ms":[3.481,1.266,5.77,3,26.733,6.029,4.151,4.707,17.849,0.833,2.07,0.843,1.954,0.742,1.938,0.694,4.008,2.171,2.051,1.887,0.777,2.414,0.55,1.401,0.681,1.044,0.51,0.6,0.547,4.438,0.294,0.361,1.419,0.331,1.734,0.28,1.75,0.356,3.835,0.598,0.916,0.562,0.95,0.708,0.957,0.528,4.175,0.294,1.829,0.326,2.64,0.273,0.303,0.315,0.275,0.317,0.865,0.285,0.275,0.272,0.255,0.296,0.292,0.31,2.956,0.533,1.117,0.562,1.124,0.662,0.788,0.537,3.686,0.311,1.895,0.36,1.873,0.31,1.705,0.403],"p50_ms":0.788,"p95_ms":4.707,"max_ms":26.733},"published_sources":80,"reserve_source":{"samples_ms":[39.238,44.745,49.666,54.062,12.918,12.198,11.519,10.754,9.7,10.105,10.436,11.091,9.382,9.978,11.418,12.048,8.901,9.522,9.998,10.593,8.12,9.297,9.853,10.896,7.926,8.567,9.275,9.458,7.266,8.089,8.544,9.484,7.499,7.963,8.903,9.725,7.547,8.274,8.568,9.613,8.003,8.349,9.074,9.274,5.705,6.567,6.982,7.454,5.57,6.337,7.177,7.958,5.379,5.864,6.274,6.728,5.706,6.03,6.908,7.206,5.617,6.419,7.39,8.287,5.762,6.385,6.923,7.326,5.421,6.349,7.181,8.077,5.471,6.438,6.984,7.863,6.451,6.805,6.613,7.037],"p50_ms":8.003,"p95_ms":12.918,"max_ms":54.062},"retention_batch":{"samples_ms":[69.608,23.678,9.059,10.969,8.891,13.011,8.276,10.62,7.636,11.453,7.852,9.439,6.88,9.76,7.125,9.948,7.177,9.616,7.558,9.384],"p50_ms":9.384,"p95_ms":23.678,"max_ms":69.608},"samples_with_waiters":75,"sampling_interval_ms":5,"source_reservations_remaining":0}')


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

    def promotion_pair(self, before_short_pass=False, after_short_pass=True):
        image = {"config_digest": PG_CONFIG, "platform": "linux/amd64", "memory_bytes": 1073741824, "nano_cpus": 2000000000, "memory_swap_bytes": 1073741824, "oom_killed": False}
        pair = []
        for label, passed in (("before", before_short_pass), ("after", after_short_pass)):
            report = fixture(label, short_pass=passed)
            verdict = validate_report(report, label, 1000, IDENTITY)
            code = 0 if verdict["workload_pass"] else 1
            validate_go_json(go_events("pass" if code == 0 else "fail"), code, verdict["workload_pass"])
            version = IDENTITY["versions"][label]
            source = {"commit": version["commit"], "tree": version["tree"], "parents": version["parents"], "application_inventory_sha256": version["application_inventory_sha256"], "application_files": len(version["application_files"]), "status": "?? internal/app/storage005_comparison_pilot_test.go\n"}
            pair.append({"version": label, "size": 1000, "verification": verdict, "go_exit": code, "namespace_role_cleanup": True, "post_source": source, "image": copy.deepcopy(image), "postgresql_settings": report["postgresql_settings"]})
        return pair

    def test_maximum_size_defaults_to_explicit_1000_pair_and_refuses_100k(self):
        self.assertEqual(admitted_sizes("1000"), [1000])
        self.assertEqual(admitted_sizes("10000"), [1000, 10000])
        for value in (None, True, 1000, "1000.0", "100000", ""):
            with self.subTest(value=value), self.assertRaises(Refused):
                admitted_sizes(value)

    def test_current_promotion_preserves_original_failed_baseline(self):
        pair = self.promotion_pair()
        original = canonical(pair)
        self.assertTrue(promotion_ready(pair, 1000, IDENTITY))
        self.assertFalse(all(w["verification"]["workload_pass"] for w in pair))
        self.assertEqual(pair[0]["verification"]["failed_stages"], ["progressive_bootstrap", "short_reconciliation"])
        self.assertEqual(canonical(pair), original)

    def test_current_5ms_failure_blocks_promotion_despite_essential_correctness(self):
        pair = self.promotion_pair(after_short_pass=False)
        self.assertTrue(pair[1]["verification"]["correctness_stages_pass"])
        self.assertFalse(promotion_ready(pair, 1000, IDENTITY))

    def test_baseline_essential_failure_or_incomplete_pair_blocks_promotion(self):
        pair = self.promotion_pair()
        pair[0]["verification"]["correctness_stages_pass"] = False
        self.assertFalse(promotion_ready(pair, 1000, IDENTITY))
        for pair in ([], self.promotion_pair()[:1], list(reversed(self.promotion_pair()))):
            self.assertFalse(promotion_ready(pair, 1000, IDENTITY))
        self.assertFalse(promotion_ready(self.promotion_pair(), 10000, IDENTITY))

    def test_promotion_requires_original_exit_cleanup_and_runtime_identity(self):
        for index, path, changed in ((0, ("go_exit",), 0), (1, ("go_exit",), 1),
                                     (1, ("namespace_role_cleanup",), False),
                                     (1, ("size",), 1000.0),
                                     (1, ("post_source", "application_inventory_sha256"), "0" * 64),
                                     (1, ("post_source", "status"), ""),
                                     (1, ("image", "oom_killed"), True),
                                     (1, ("verification", "verified_measurements"), False)):
            with self.subTest(index=index, path=path):
                pair = self.promotion_pair()
                target = pair[index]
                for key in path[:-1]:
                    target = target[key]
                target[path[-1]] = changed
                self.assertFalse(promotion_ready(pair, 1000, IDENTITY))

    def test_matched_but_changed_postgres_settings_or_image_refuse_promotion(self):
        for field, key, changed in (("postgresql_settings", "fsync", "off"),
                                    ("image", "config_digest", "sha256:" + "0" * 64),
                                    ("image", "memory_bytes", 0)):
            pair = self.promotion_pair()
            for work in pair:
                work[field][key] = changed
            self.assertFalse(promotion_ready(pair, 1000, IDENTITY))

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

    def test_actual_go_nil_errors_preserve_report_and_original_5ms_failures(self):
        value = fixture("before", short_pass=False)
        value["stages"]["foreground_maintenance"] = copy.deepcopy(ACTUAL_FOREGROUND_NULL)
        original = canonical(value)
        proof = validate_report(value, "before", 1000, IDENTITY)
        self.assertTrue(proof["verified_measurements"])
        self.assertTrue(proof["correctness_stages_pass"])
        self.assertFalse(proof["workload_pass"])
        self.assertEqual(proof["failed_stages"], ["progressive_bootstrap", "short_reconciliation"])
        self.assertEqual(canonical(value), original)
        self.assertIsNone(value["stages"]["foreground_maintenance"]["errors"])
        value["pass"] = True
        self.rejected(value, label="before")

    def test_nil_errors_require_present_field_and_preserve_foreground_oracles(self):
        for invalid in (False, 0, "", {}, ["operation failed"]):
            value = fixture()
            value["stages"]["foreground_maintenance"]["errors"] = invalid
            self.rejected(value)
        value = fixture()
        del value["stages"]["foreground_maintenance"]["errors"]
        self.rejected(value)
        for key, changed in (("admission_workers", 3), ("operations_per_worker", 19),
                             ("published_sources", 79), ("source_reservations_remaining", 1),
                             ("counters_match_actual_ledger", False), ("pass", False)):
            value = fixture()
            value["stages"]["foreground_maintenance"].update(errors=None)
            value["stages"]["foreground_maintenance"][key] = changed
            self.rejected(value)

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
