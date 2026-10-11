"""Pure fixed-source pilot checks; importing this module never starts a child."""
from __future__ import annotations

import hashlib
import json
import math

BEFORE = "63873e935626d51087d0a26b84f1de9a7ed86a28"
AFTER = "f80cb9bba29f2ba97b37152bbf3388735d4a3173"
APP_HASHES = {"before": "a1aa7b968c06eaeb075d2cd73f8afbf309a1bc12121d1a3e598ba2561e785f90", "after": "57f97d213b17785c76079e5a0adaaffcc8a7f2350a911af947a2ddd536a04cea"}
SQL_HASHES = {"before": "f24b6db8d33ec3b30ac7f26c21fb5874add71f26ea128b1d0468fd20d841b474", "after": "8f40530566bd0bd9bf3ae12ca9604ff20baa5a58a932ea244ee9b5c01ebe8fb7"}
IDENTITY_SHA256 = "bfad2c0c3f1c636f10a62d33aca077eff1a728f61281d86c65256dd57cbf8b5f"
STAGES = {"source_ttl", "foreground_dml", "held_fence", "persisted_audit", "foreground_maintenance", "short_reconciliation", "progressive_bootstrap"}
PG_SETTINGS = {"server_version_num": "170011", "shared_buffers": "128MB", "work_mem": "4MB", "max_connections": "32", "jit": "off", "fsync": "on", "synchronous_commit": "on"}
DML_SQL = {
    "job_status_only": "UPDATE jobs SET status='failed',updated_at='2001-01-01' WHERE id='retained-job-1'",
    "job_membership_insert": "INSERT INTO jobs(id,owner,source_id,request,items) VALUES('comparison-dml-job','comparison','retained-source-1','{}','[]')",
    "preview_membership_insert": "INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('comparison-dml-preview','comparison','preview',1,'2100-01-01','retained-source-1')",
}
PG_INDEX = "sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826"
PG_CHILD = "sha256:66aafa11cf15800a3c94763f7e11d1e7b2e37e5e84bc4ce027cb6e1e4bfaf4df"
PG_CONFIG = "sha256:589f742565e4f6d22149176b032974f55f8201a8a37b71ed587ec5c6902e689f"


class Refused(ValueError):
    pass


def require(condition, reason):
    if not condition:
        raise Refused(reason)


def canonical(value):
    return (json.dumps(value, indent=2, sort_keys=True, allow_nan=False) + "\n").encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def loads(raw):
    def pairs(items):
        value = {}
        for key, item in items:
            require(key not in value, "duplicate_json_key")
            value[key] = item
        return value
    try:
        return json.loads(raw, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(Refused("nonfinite_json")))
    except (json.JSONDecodeError, UnicodeDecodeError):
        raise Refused("invalid_json") from None


def number(value):
    return type(value) in (int, float) and math.isfinite(value) and value >= 0


def latency(value, count):
    require(isinstance(value, dict), "latency_shape")
    samples = value.get("samples_ms")
    require(isinstance(samples, list) and len(samples) == count and all(number(x) for x in samples), "latency_samples")
    ordered = sorted(samples)
    expected = (ordered[math.ceil(count * .5) - 1], ordered[math.ceil(count * .95) - 1], ordered[-1])
    require(tuple(value.get(x) for x in ("p50_ms", "p95_ms", "max_ms")) == expected, "latency_percentiles")


def validate_identity(value):
    require(isinstance(value, dict) and value.get("format") == 1 and value.get("repository") == "Kay0k1/cutmyvideo-core", "identity_shape")
    require(sha(canonical(value)) == IDENTITY_SHA256, "reviewed_identity_digest")
    require(value.get("sizes") == [1000, 10000] and value.get("samples") == 20 and value.get("platform") == "linux/amd64", "pilot_admission")
    require(value.get("postgres") == {"image": "docker.io/library/postgres@" + PG_INDEX, "index_digest": PG_INDEX, "amd64_manifest_digest": PG_CHILD, "config_digest": PG_CONFIG}, "postgres_pin")
    require(set(value.get("versions", {})) == {"before", "after"}, "version_set")
    for label, commit, schema, count in (("before", BEFORE, 4, 164), ("after", AFTER, 5, 172)):
        v = value["versions"][label]
        require(v.get("commit") == commit and v.get("schema") == schema, "fixed_commit")
        inventory = v.get("application_files")
        require(isinstance(inventory, dict) and len(inventory) == count and sha(canonical(inventory)) == APP_HASHES[label] == v.get("application_inventory_sha256"), "application_inventory")
        require(sha(v.get("selector_sql", "").encode()) == SQL_HASHES[label] == v.get("selector_sql_sha256"), "actual_sql_binding")
    return value


def admitted_sizes(maximum_size):
    require(type(maximum_size) is str and maximum_size in ("1000", "10000"), "reviewed_maximum_size")
    return [1000] if maximum_size == "1000" else [1000, 10000]


def promotion_ready(workloads, size, identity):
    """Promote verified current work without changing a failed baseline verdict."""
    if type(size) is not int or size not in (1000, 10000) or not isinstance(workloads, list):
        return False
    pair = [w for w in workloads if isinstance(w, dict) and type(w.get("size")) is int and w["size"] == size]
    if len(pair) != 2 or [w.get("version") for w in pair] != ["before", "after"]:
        return False
    for work in pair:
        label = work["version"]
        verdict = work.get("verification", {})
        if not isinstance(verdict, dict) or verdict.get("verified_measurements") is not True or verdict.get("version") != label or verdict.get("size") != size:
            return False
        if verdict.get("correctness_stages_pass") is not True or type(verdict.get("workload_pass")) is not bool:
            return False
        if label == "after" and (verdict["workload_pass"] is not True or verdict.get("failed_stages") != []):
            return False
        if type(work.get("go_exit")) is not int or work["go_exit"] != (0 if verdict["workload_pass"] else 1) or work.get("namespace_role_cleanup") is not True:
            return False
        expected = identity["versions"][label]
        post_source = {"commit": expected["commit"], "tree": expected["tree"], "parents": expected["parents"], "application_inventory_sha256": expected["application_inventory_sha256"], "application_files": len(expected["application_files"]), "status": "?? internal/app/storage005_comparison_pilot_test.go\n"}
        if work.get("post_source") != post_source:
            return False
    image = pair[0].get("image")
    return (isinstance(image, dict) and image == pair[1].get("image")
            and image.get("config_digest") == PG_CONFIG and image.get("platform") == "linux/amd64"
            and image.get("memory_bytes") == 1073741824 and image.get("nano_cpus") == 2000000000
            and image.get("memory_swap_bytes") == 1073741824 and image.get("oom_killed") is False
            and isinstance(pair[0].get("postgresql_settings"), dict)
            and pair[0]["postgresql_settings"] == pair[1].get("postgresql_settings")
            and {k: pair[0]["postgresql_settings"].get(k) for k in PG_SETTINGS} == PG_SETTINGS)


def validate_image(container_raw, image_raw):
    values = [loads(line) for line in container_raw.splitlines()]
    require(len(values) == 8 and values[0] in (PG_CONFIG, PG_INDEX) and values[1] == "docker.io/library/postgres@" + PG_INDEX and values[2:] == ["linux", 1073741824, 2000000000, 1073741824, False, True], "container_platform_limits")
    image = [loads(line) for line in image_raw.splitlines()]
    accepted_repos = {repo + "@" + PG_INDEX for repo in ("postgres", "docker.io/library/postgres", "registry-1.docker.io/library/postgres")}
    require(len(image) == 4 and image[0] in (PG_CONFIG, PG_INDEX) and image[1:3] == ["linux", "amd64"] and isinstance(image[3], list) and any(d in accepted_repos for d in image[3]), "image_repo_platform")
    return {"container_image_identity": values[0], "image_store_identity": image[0], "reference": values[1], "platform": "linux/amd64", "index_digest": PG_INDEX, "amd64_manifest_digest": PG_CHILD, "config_digest": PG_CONFIG, "memory_bytes": values[3], "nano_cpus": values[4], "memory_swap_bytes": values[5], "oom_killed": values[6]}


def query(value, sql=None):
    require(isinstance(value, dict) and (sql is None or value.get("sql") == sql), "query_sql")
    latency(value.get("client_round_trip"), 20)
    latency(value.get("postgres_execution"), 20)
    plans = value.get("explain_analyze_buffers")
    require(isinstance(plans, list) and len(plans) == 20, "plan_count")
    for index, raw in enumerate(plans):
        require(isinstance(raw, list) and len(raw) == 1 and isinstance(raw[0], dict) and isinstance(raw[0].get("Plan"), dict), "plan_shape")
        execution = raw[0].get("Execution Time")
        require(number(execution) and execution == value["postgres_execution"]["samples_ms"][index], "raw_execution_time")


def validate_report(value, label, size, identity):
    require(label in ("before", "after") and type(size) is int and size in (1000, 10000), "pilot_size")
    v = identity["versions"][label]
    require(isinstance(value, dict) and type(value.get("format")) is int and value.get("format") == 1 and value.get("kind") == "storage005-comparison-pilot", "report_shape")
    require(value.get("size") == size and value.get("schema") == v["schema"] and value.get("complete") is True and type(value.get("pass")) is bool, "report_completion")
    settings = value.get("postgresql_settings", {})
    require(value.get("sql_sha256") == SQL_HASHES[label] and isinstance(settings, dict) and {k: settings.get(k) for k in PG_SETTINGS} == PG_SETTINGS and isinstance(settings.get("server_version"), str) and settings["server_version"].startswith("17.11"), "report_source_environment")
    role = value.get("runtime_role", {})
    require(isinstance(role, dict), "runtime_role_shape")
    require(role.get("pass") is True and role.get("superuser") is False and role.get("schema_create") is False and role.get("migration_ledger_insert") is False and role.get("ordinary_table_dml") is True and role.get("function_execute_granted") is False and role.get("session_login_is_fixture_admin") is True, "effective_runtime_grants")
    initial = value.get("initial_rows", {})
    for table, rows in (("sources", size + 200), ("jobs", size + 200), ("reservations", 200), ("files", size + 200 + max(200, size // 20))):
        require(isinstance(initial.get(table), dict) and initial[table].get("rows") == rows, "initial_fixture_rows")
    stages = value.get("stages")
    require(isinstance(stages, dict) and set(stages) == STAGES and all(isinstance(s, dict) and type(s.get("pass")) is bool for s in stages.values()), "stage_set")
    ttl = stages["source_ttl"]
    require(ttl.get("pass") is True and ttl.get("samples") == 20 and ttl.get("before") == ttl.get("after") == initial and ttl.get("rollback_after_each_sample") is True, "ttl_rollback")
    require(ttl.get("sample_rows") == [{"retained_pinned_remaining": size, "expired_remaining": 0, "eligible_deleted": 200}] * 20, "ttl_reference_oracles")
    query(ttl.get("query"), v["selector_sql"])
    latency(ttl.get("fence"), 20)
    dml = stages["foreground_dml"]
    require(dml.get("pass") is True and dml.get("samples_per_operation") == 20 and dml.get("before") == dml.get("after") == initial, "dml_rollback")
    require(isinstance(dml.get("queries"), list) and all(isinstance(q, dict) for q in dml["queries"]), "dml_shape")
    require([q.get("name") for q in dml["queries"]] == ["job_status_only", "job_membership_insert", "preview_membership_insert"], "dml_operations")
    for q in dml["queries"]:
        query(q, DML_SQL[q["name"]])
    held = stages["held_fence"]
    attempts = held.get("attempts", [])
    require(isinstance(attempts, list) and all(isinstance(a, dict) for a in attempts), "fence_shape")
    require(len(attempts) == 2 and [a.get("budget_ms") for a in attempts] == [5, 50] and all(number(a.get("wall_ms")) and type(a.get("deadline")) is bool and type(a.get("unexpected_acquisition")) is bool for a in attempts), "fence_attempts")
    require(type(held.get("acquired_after_release")) is bool and held.get("wall_limit_claimed") is False, "fence_recovery")
    require(held["pass"] == (all(a["deadline"] and not a["unexpected_acquisition"] for a in attempts) and held["acquired_after_release"]), "fence_verdict")
    audit = stages["persisted_audit"]
    if label == "before":
        require(audit == {"applicable": False, "reason": "schema004 has no source-pin audit", "pass": True}, "004_audit_na")
    else:
        require(audit.get("applicable") is True and audit.get("pass") is True and audit.get("hot_job_count") == audit.get("hot_preview_count") == 128 and audit.get("healthy_successor_repaired") is True and audit.get("complete_child_audit_claimed") is False and audit.get("audit_only_fixture_removed") is True, "005_audit_oracle")
        calls = audit.get("calls", [])
        require(isinstance(calls, list) and all(isinstance(c, dict) for c in calls), "audit_call_shape")
        require(len(calls) == 10 and [c.get("unit") for c in calls] == list(range(1, 11)) and all(number(c.get("elapsed_ms")) and c.get("done") is False and type(c.get("repaired_eligible")) is bool and "error" not in c for c in calls), "audit_units")
        require(isinstance(audit.get("progress"), dict), "audit_progress_shape")
        partial = audit["progress"].get("partial", [])
        require(isinstance(partial, list) and all(isinstance(p, dict) for p in partial), "audit_partial_shape")
        require(len(partial) == 1 and partial[0].get("source_id") == "comparison-a-hot" and partial[0].get("job_count") == partial[0].get("preview_count") == 128 and partial[0].get("job_done") is False and partial[0].get("preview_done") is False, "durable_partial_audit")
    foreground = stages["foreground_maintenance"]
    errors = foreground.get("errors")
    require(foreground.get("admission_workers") == 4 and foreground.get("operations_per_worker") == 20 and "errors" in foreground and (errors is None or isinstance(errors, list)), "foreground_workload")
    # Unchanged capacityConcurrent marshals a nil []string as explicit JSON null.
    # Normalize locally; retain the original report and reject a missing field.
    errors = [] if errors is None else errors
    expected_foreground = not errors and foreground.get("published_sources") == 80 and foreground.get("source_reservations_remaining") == 0 and foreground.get("counters_match_actual_ledger") is True
    require(foreground["pass"] == expected_foreground, "foreground_verdict")
    if expected_foreground:
        for key, count in (("reserve_source", 80), ("durable_source_publication", 80), ("retention_batch", 20), ("persisted_metadata_hit", 80)):
            latency(foreground.get(key), count)
    for name in ("short_reconciliation", "progressive_bootstrap"):
        stage = stages[name]
        progress = stage.get("registered_after_each_cycle")
        require(stage.get("cycles") == 32 and stage.get("cycle_budget_ms") == 5 and isinstance(progress, list) and len(progress) == 32 and all(type(x) is int and 0 <= x <= size + 1024 for x in progress), "original_5ms_cycles")
        latency(stage.get("latency"), 32)
        if name == "short_reconciliation":
            require(stage.get("physical_zero_byte_entries") == size and stage.get("physical_small_files") == 1024 and stage.get("physical_files") == size + 1024 and stage.get("registered_before_full_scan") == progress[-1], "reconcile_fixture")
            passed = (progress[-1] == size + 1024 or progress[-1] >= progress[7] + min(size + 1024 - progress[7], 512)) and stage.get("unchanged_scan_preserves_tuple_xmin") is True
        else:
            require(stage.get("registered_before_final_pass") == progress[-1], "bootstrap_progress")
            passed = stage.get("initialization_markers") == 1 and stage.get("final_registered_files") == size + 1024 and (progress[-1] > progress[0] or progress[-1] == size + 1024)
        # The unchanged bootstrap helper also requires a successful real
        # ReserveSource. That private boolean is not exported by its report;
        # preserve conservative FAIL even when the visible counts look good.
        require(stage["pass"] == passed if name == "short_reconciliation" else (not stage["pass"] or passed), "original_5ms_verdict")
    failed = sorted(name for name, stage in stages.items() if not stage["pass"])
    require(isinstance(value.get("failures"), list) and sorted(value["failures"]) == failed and len(set(value["failures"])) == len(failed) and value["pass"] == (not failed), "whole_workload_verdict")
    correctness = all(stages[name]["pass"] for name in STAGES - {"short_reconciliation", "progressive_bootstrap"})
    return {"verified_measurements": True, "workload_pass": value["pass"], "correctness_stages_pass": correctness, "failed_stages": failed, "size": size, "version": label, "performance_claim": False}


def validate_go_json(raw, exit_code, workload_pass):
    events = [loads(line) for line in raw.splitlines() if line.strip()]
    require(events and all(isinstance(e, dict) for e in events), "go_json_shape")
    require(all(e.get("Package") == "github.com/Kay0k1/cutmyvideo-core/internal/app" for e in events), "go_package")
    require(all("Test" not in e or e["Test"] == "TestStorage005ComparisonPilot" for e in events), "go_test_scope")
    require(sum(e.get("Test") == "TestStorage005ComparisonPilot" and e.get("Action") == "run" for e in events) == 1, "go_test_run")
    terminal = [e for e in events if e.get("Test") == "TestStorage005ComparisonPilot" and e.get("Action") in ("pass", "fail", "skip")]
    require(len(terminal) == 1 and terminal[0]["Action"] == ("pass" if workload_pass else "fail") and exit_code == (0 if workload_pass else 1), "actual_go_terminal")
    require(not any(e.get("Action") == "skip" for e in events), "skipped_pilot")
    packages = [e for e in events if "Test" not in e and e.get("Action") in ("pass", "fail")]
    require(len(packages) == 1 and packages[0]["Action"] == terminal[0]["Action"], "go_package_terminal")
    return True
