"""Synthetic migration-evidence controls; no PostgreSQL, Go or child operation."""
import copy
import importlib
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

ACTUAL_ATTEMPTS = []


def deny_actual_operations(event, args):
    if event.startswith(("subprocess.", "socket.")) or event in {
            "os.system", "os.fork", "os.forkpty", "os.posix_spawn", "os.exec", "os.spawn"}:
        ACTUAL_ATTEMPTS.append(event)
        raise AssertionError("pure populated controls deny child/network")


sys.addaudithook(deny_actual_operations)
from protocol import (CALLS, COUNTS, INSERT_QUERY, SNAPSHOTS, Refused, canonical,
                      compare_snapshots, digest, loads, sha, validate_call,
                      validate_fixture, validate_identity, validate_oracles,
                      validate_migration_owner, validate_resource_sample,
                      validate_snapshot, validate_witness)

IDENTITY = loads(Path(__file__).with_name("identity.json").read_bytes())


def synthetic_digest(projection, count, xmin):
    return {"rows": count, "utf8_bytes": count, "framed_bytes": count * 9,
            "sha256": "a" * 64 if count else sha(b""),
            "columns": projection["columns"], "order": projection["order"], "xmin": xmin}


def snapshot(version):
    legacy = {table: synthetic_digest(projection, COUNTS.get(table, 4), table != "sources")
              for table, projection in IDENTITY["schema004"].items()}
    full = {}
    for table, projection in IDENTITY["schema00" + str(version)].items():
        count = COUNTS.get(table, version)
        if table == "source_retention_audit": count = 1
        elif table == "source_pin_audit_progress": count = 0
        full[table] = synthetic_digest(projection, count, True)
    ledger = [{"row": {"position": i, "checksum": "b" * 64, "version": "synthetic-" + str(i),
                       "applied_at": "2000-01-01T00:00:00+00:00"}, "xmin": "123"}
              for i in range(1, version + 1)]
    if version == 5: ledger[4]["row"]["checksum"] = IDENTITY["migration005_lf_sha256"]
    return {"version": version, "catalog_checked": True, "legacy004": legacy, "full": full,
            "source_xmin": synthetic_digest({"columns": ["id", "xmin"], "order": ["id COLLATE C"]}, 100000, True),
            "catalog_sha256": "c" * 64,
            "catalog_file_sha256": sha(canonical({"synthetic_catalog_version": version})),
            "ledger_original_rows": ledger, "settings": {"TimeZone": "UTC", "DateStyle": "ISO, MDY",
            "bytea_output": "hex", "isolation": "repeatable read", "access": "on"}}


def call(name):
    blocked = name == "deadline-blocked-upgrade"
    return {"call": name, "caller": "context.Background", "startup_budget_ns": 30000000000,
            "watchdog_ns": 45000000000, "elapsed_ns": 30010000000 if blocked else 1000000000,
            "nonnil_store": not blocked, "error": {"deadline": blocked, "canceled": False,
            "chain": ["syntheticDeadline"] if blocked else [],
            **({"message": "synthetic context deadline"} if blocked else {})}}


def witness(schema):
    return {"pending_ordinal_source_derived": 5, "pre_call_exact_ledger_position": 4,
            "immutable005_checksum": IDENTITY["migration005_lf_sha256"],
            "observed": {"startup_pid": 123, "blocker_pid": 456, "query": INSERT_QUERY,
            "startup_application": schema + "_startup",
            "startup_login": schema + "_owner",
            "relation": '"' + schema + '"."app_schema_migrations"', "waiting_mode": "RowExclusiveLock",
            "waiting_granted": False, "blocker_mode": "ShareLock", "blocker_granted": True,
            "blocking_pids": [456]}}


def oracles(version):
    counts = dict(COUNTS, app_schema_migrations=version, app_schema_versions=version)
    result = {"counts": counts, "stored_bytes": 2000065, "reserved_bytes": 2025018,
              "actual_accounting_equal": True}
    if version == 5:
        counts.update(source_retention_audit=1, source_pin_audit_progress=0)
        jobs, previews = [0, 1, 1, 1, 1, 1, 1, 2], [0, 0, 1, 2, 0, 1, 0, 0]
        result.update(groups=[[g, 12500, jobs[g] * 12500, previews[g] * 12500] for g in range(8)],
                      pin_mismatch_count=0, eligible_sources=12500, job_pins=100000, preview_pins=50000)
    return result


def synthetic_files(root):
    schemas = ["populated100k_success_" + "a" * 32, "populated100k_deadline_" + "b" * 32]
    def write(name, value): (root / name).write_bytes(canonical(value))
    write("fixture-final.json", {"OpenStore_calls": 4, "maximum_calls": 4, "failed": False,
          "complete": True, "remaining_schemas": 0, "remaining_roles": 0, "owned_schemas": schemas,
          "owned_roles": [schema + "_owner" for schema in schemas], "full_raw_row_archive": False})
    for name in CALLS: write(name + "-open.json", call(name))
    for name, version in SNAPSHOTS:
        write(name + "-snapshot.json", snapshot(version))
        write(name + "-catalog.json", {"synthetic_catalog_version": version})
    for name, version in [("success004", 4), ("success005-after-upgrade", 5), ("deadline004", 4),
                          ("deadline004-after-deadline", 4), ("deadline005-after-labelled-recovery", 5)]:
        write(name + "-oracles.json", oracles(version))
    write("deadline-ledger-insert-lock-witness.json", witness(schemas[1]))
    for name, schema in (("success004", schemas[0]), ("success005-after-upgrade", schemas[0]),
                         ("success005-after-restart", schemas[0]), ("deadline004", schemas[1]),
                         ("deadline005-after-labelled-recovery", schemas[1])):
        write(name + "-migration-owner.json", migration_owner(schema))
    for label, schema in zip(("success", "deadline"), schemas):
        write(label + "-namespace-cleanup.json", {"schema": schema, "remaining": 0,
              "role": schema + "_owner", "remaining_roles": 0})


def migration_owner(schema):
    return {"current_user": schema + "_owner", "session_user": schema + "_owner",
            "role": schema + "_owner", "schema": schema, "login": True,
            "superuser": False, "create_database": False, "create_role": False,
            "inherit": False, "replication": False, "bypass_rls": False,
            "owns_schema": True, "schema_create": True, "database_create": False,
            "public_schema_create": False, "owned_schema_count": 1,
            "other_schema_create_count": 0, "membership_count": 0,
            "object_owner_mismatch_count": 0, "runtime_dml_least_privilege_checked": False}


class PopulatedProtocolControls(unittest.TestCase):
    @classmethod
    def tearDownClass(cls):
        assert not ACTUAL_ATTEMPTS, ACTUAL_ATTEMPTS
        print("pure populated child/network attempts: 0", flush=True)

    def test_frozen_current_identity_and_source_only_nulls(self):
        validate_identity(IDENTITY)
        altered = copy.deepcopy(IDENTITY); altered["actual_fields"]["digests"] = {}
        with self.assertRaises(Refused): validate_identity(altered)

    def test_runner_import_starts_no_child_or_network(self):
        importlib.import_module("run_once")
        self.assertEqual(ACTUAL_ATTEMPTS, [])

    def test_short_original_stdout_and_stderr_are_visible_before_capture_returns(self):
        runner = importlib.import_module("run_once")
        stdout, stderr = b"d594cc4681dc1cab61664cec8c92bdf9a3e9444c\n", b"short diagnostic\n"
        class FakeChild:
            pid = 123
            returncode = 0
            def __init__(self):
                self.stdout = io.BytesIO(stdout)
                self.stderr = io.BytesIO(stderr)
            def poll(self):
                return self.returncode
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            run = runner.OnceRun(root)
            with mock.patch.object(runner.subprocess, "Popen", return_value=FakeChild()) as fake:
                result, raw = run.capture(["synthetic-command"], "short-original")
            fake.assert_called_once()
            self.assertEqual(raw, stdout)
            self.assertEqual((root / result["stdout"]).read_bytes(), stdout)
            self.assertEqual((root / result["stderr"]).read_bytes(), stderr)
            self.assertEqual((result["captured_stdout_bytes"], result["captured_stderr_bytes"]),
                             (len(stdout), len(stderr)))
            self.assertTrue(result["reaped"])
            self.assertFalse(result["overflow"] or result["timeout"] or result["interrupted"])
        self.assertEqual(ACTUAL_ATTEMPTS, [])

    def test_full004_and005_projection_contracts(self):
        for version in (4, 5): validate_snapshot(snapshot(version), version, IDENTITY)

    def test_unsigned64_framing_and_empty_digest_refuse_drift(self):
        value = synthetic_digest({"columns": ["id"], "order": ["id"]}, 1, True)
        value["framed_bytes"] += 1
        with self.assertRaises(Refused): digest(value, ["id"], ["id"], 1, True)
        value = synthetic_digest({"columns": [], "order": []}, 0, True); value["sha256"] = "a" * 64
        with self.assertRaises(Refused): digest(value, [], [], 0, True)

    def test_upgrade_allows_source_xmin_change_but_preserves_old_rows(self):
        before, after = snapshot(4), snapshot(5)
        after["source_xmin"]["sha256"] = "d" * 64
        compare_snapshots(before, after, "upgrade")
        after["legacy004"]["jobs"]["sha256"] = "e" * 64
        with self.assertRaises(Refused): compare_snapshots(before, after, "upgrade")

    def test_original_ledger_applied_at_and_xmin_survive(self):
        for key in ("applied_at", "checksum"):
            before, after = snapshot(4), snapshot(5)
            after["ledger_original_rows"][1]["row"][key] = "changed"
            with self.assertRaises(Refused): compare_snapshots(before, after, "upgrade")
        before, after = snapshot(4), snapshot(5); after["ledger_original_rows"][0]["xmin"] = "124"
        with self.assertRaises(Refused): compare_snapshots(before, after, "upgrade")

    def test_atomic_rollback_requires_source_xmin_catalog_and_rows(self):
        before = snapshot(4)
        compare_snapshots(before, copy.deepcopy(before), "rollback")
        after = copy.deepcopy(before); after["source_xmin"]["sha256"] = "d" * 64
        with self.assertRaises(Refused): compare_snapshots(before, after, "rollback")
        after = copy.deepcopy(before); after["catalog_sha256"] = "d" * 64
        with self.assertRaises(Refused): compare_snapshots(before, after, "rollback")

    def test_restart_cannot_repeat_backfill_or_replace_audit(self):
        for table in ("sources", "source_retention_audit"):
            before, after = snapshot(5), snapshot(5); after["full"][table]["sha256"] = "d" * 64
            with self.assertRaises(Refused): compare_snapshots(before, after, "restart")

    def test_real_background_success_cannot_exceed30s(self):
        for changed in ({"elapsed_ns": 30000000001}, {"caller": "context.WithTimeout"},
                        {"nonnil_store": False}, {"error": {"deadline": True}}):
            value = call("success-upgrade"); value.update(changed)
            with self.assertRaises(Refused): validate_call(value, "success-upgrade")

    def test_deadline_requires_real_cause_and29to32s_return(self):
        for changed in ({"elapsed_ns": 1000000000}, {"elapsed_ns": 32000000001},
                        {"error": {"deadline": False, "message": "timeout", "chain": ["fake"]}},
                        {"nonnil_store": True}):
            value = call("deadline-blocked-upgrade"); value.update(changed)
            with self.assertRaises(Refused): validate_call(value, "deadline-blocked-upgrade")

    def test_witness_requires_query_relation_distinct_PIDs_and_link(self):
        schema = "populated100k_deadline_" + "b" * 32; value = witness(schema)
        validate_witness(value, schema, IDENTITY)
        for key, changed in (("query", "SELECT pg_sleep(30)"), ("relation", "foreign.ledger"),
                             ("waiting_granted", True), ("blocker_mode", "AccessShareLock"),
                             ("blocking_pids", []), ("startup_pid", 456), ("startup_login", "postgres")):
            altered = copy.deepcopy(value); altered["observed"][key] = changed
            with self.assertRaises(Refused): validate_witness(altered, schema, IDENTITY)

    def test_migration_owner_checks_real_login_and_schema_only_permissions(self):
        schema = "populated100k_success_" + "a" * 32
        validate_migration_owner(migration_owner(schema), schema)
        for field, changed in (("current_user", "postgres"), ("session_user", "postgres"),
                               ("superuser", True), ("create_role", True), ("database_create", True),
                               ("public_schema_create", True), ("other_schema_create_count", 1),
                               ("owned_schema_count", 2), ("membership_count", 1),
                               ("object_owner_mismatch_count", 1), ("owned_schema_count", True),
                               ("runtime_dml_least_privilege_checked", True)):
            value = migration_owner(schema); value[field] = changed
            with self.assertRaises(Refused): validate_migration_owner(value, schema)

    def test_actual_resource_sample_refuses_overflow_oom_swap_and_missing_final_fields(self):
        sample = {"phase": "after-fixture", "volume_bytes": 123, "memory_current_bytes": 1000,
                  "memory_peak_bytes": 2000, "swap_current_bytes": 0, "pg_disk_free_bytes": 4294967296,
                  "memory_events": {"oom": 0, "oom_kill": 0}}
        validate_resource_sample(sample)
        for field, changed in (("volume_bytes", 2147483649), ("memory_peak_bytes", 1073741825),
                               ("memory_current_bytes", 2001), ("swap_current_bytes", 1),
                               ("pg_disk_free_bytes", 4294967295), ("memory_events", {"oom": 1, "oom_kill": 0}),
                               ("memory_peak_bytes", None), ("volume_bytes", True)):
            value = copy.deepcopy(sample); value[field] = changed
            with self.assertRaises(Refused): validate_resource_sample(value)

    def test_terminal_jobs_exact_preview_pins_and_accounting(self):
        validate_oracles(oracles(5), 5)
        for field, changed in (("job_pins", 87500), ("preview_pins", 87500),
                               ("eligible_sources", 25000), ("reserved_bytes", 2025000),
                               ("pin_mismatch_count", False)):
            value = oracles(5); value[field] = changed
            with self.assertRaises(Refused): validate_oracles(value, 5)

    def test_persisted_cache_scan_retry_and_counter_counts_survive(self):
        for table in ("source_metadata_cache", "storage_scan_progress", "storage_work_cleanup", "storage_counters"):
            value = oracles(4); value["counts"][table] = 0
            with self.assertRaises(Refused): validate_oracles(value, 4)
        value = oracles(4); value["counts"]["storage_counters"] = True
        with self.assertRaises(Refused): validate_oracles(value, 4)

    def test_missing_old_columns_xmin_or_changed005_checksum_refuse(self):
        value = snapshot(4); value["legacy004"]["jobs"]["columns"] = []
        with self.assertRaises(Refused): validate_snapshot(value, 4, IDENTITY)
        value = snapshot(4); value["legacy004"]["jobs"]["xmin"] = False
        with self.assertRaises(Refused): validate_snapshot(value, 4, IDENTITY)
        value = snapshot(5); value["ledger_original_rows"][4]["row"]["checksum"] = "0" * 64
        with self.assertRaises(Refused): validate_snapshot(value, 5, IDENTITY)

    def test_duplicate_keys_and_nonfinite_metadata_refuse(self):
        for raw in ('{"complete":false,"complete":true}', '{"elapsed_ns":NaN}'):
            with self.assertRaises(Refused): loads(raw)

    def test_six_synthetic_snapshots_four_calls_do_not_claim_runtime(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); synthetic_files(root)
            proof = validate_fixture(root, IDENTITY)
            self.assertEqual(proof["startup_capacity_sample_count"], 1)
            self.assertFalse(proof["full_raw_row_archive"])

    def test_original_catalog_mutation_or_missing_witness_refuses(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); synthetic_files(root)
            (root / "success004-before-catalog.json").write_bytes(b'{"changed":true}\n')
            with self.assertRaises(Refused): validate_fixture(root, IDENTITY)
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); synthetic_files(root)
            name = "success005-after-restart"
            raw = canonical({"changed": True})
            (root / (name + "-catalog.json")).write_bytes(raw)
            saved = loads((root / (name + "-snapshot.json")).read_bytes())
            saved["catalog_file_sha256"] = sha(raw)
            (root / (name + "-snapshot.json")).write_bytes(canonical(saved))
            with self.assertRaises(Refused): validate_fixture(root, IDENTITY)
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); synthetic_files(root)
            (root / "deadline-ledger-insert-lock-witness.json").unlink()
            with self.assertRaises(Refused): validate_fixture(root, IDENTITY)


if __name__ == "__main__":
    unittest.main()
