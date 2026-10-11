"""Pure fixed-source pilot checks; importing this module never starts a child."""
from __future__ import annotations

import hashlib
import json
import math
import re

BEFORE = "63873e935626d51087d0a26b84f1de9a7ed86a28"
AFTER = "d594cc4681dc1cab61664cec8c92bdf9a3e9444c"
APP_HASHES = {"before": "a1aa7b968c06eaeb075d2cd73f8afbf309a1bc12121d1a3e598ba2561e785f90", "after": "d1ae273e0d56212ef549f029970cd4ea1f33276faa7e4e11c88127e3aeeacef6"}
SQL_HASHES = {"before": "f24b6db8d33ec3b30ac7f26c21fb5874add71f26ea128b1d0468fd20d841b474", "after": "e85e5fab8f20375a9fad988f69553f94f17570de964d8f55c46030f36d4bc019"}
IDENTITY_SHA256 = "9f8a9492358a36796d7b5bd8bcb9aa1bf51378fb41f2e335fc8df7fed83185a4"
STAGES = {"source_ttl", "foreground_dml", "held_fence", "persisted_audit", "foreground_maintenance", "normal_maintenance", "normal_api_bootstrap"}
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
    for label, commit, schema, count in (("before", BEFORE, 4, 164), ("after", AFTER, 5, 173)):
        v = value["versions"][label]
        require(v.get("commit") == commit and v.get("schema") == schema, "fixed_commit")
        inventory = v.get("application_files")
        require(isinstance(inventory, dict) and len(inventory) == count and sha(canonical(inventory)) == APP_HASHES[label] == v.get("application_inventory_sha256"), "application_inventory")
        require(sha(v.get("selector_sql", "").encode()) == SQL_HASHES[label] == v.get("selector_sql_sha256"), "actual_sql_binding")
        source = v.get("source_files")
        require(isinstance(source, dict) and len(source) == v.get("source_file_count") and sha(canonical(source)) == v.get("source_inventory_sha256"), "whole_source_inventory")
        require({k: item for k, item in source.items() if k.startswith(("cmd/", "internal/", "pkg/")) or k in ("go.mod", "go.sum")} == inventory, "whole_application_projection")
    require(value["versions"]["after"]["source_file_count"] == 391 and value.get("profile") == "normal10s5s" and value.get("maximum_operational_calls_per_stage") == 8 and value.get("caller_budgets_ms") == {"normal_maintenance":10000,"normal_api_bootstrap":5000}, "new_normal_identity")
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
        post_source = {"commit": expected["commit"], "tree": expected["tree"], "parents": expected["parents"], "application_inventory_sha256": expected["application_inventory_sha256"], "application_files": len(expected["application_files"]), "status": "?? internal/app/storage_normal_capacity_pilot_test.go\n"}
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
    require(isinstance(value, dict) and type(value.get("format")) is int and value.get("format") == 1 and value.get("kind") == "storage-normal-capacity-pilot", "report_shape")
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
    before_protected, after_protected = foreground.get("protected_before"), foreground.get("protected_after")
    require(isinstance(before_protected, dict) and isinstance(after_protected, dict), "normal_foreground_snapshot")
    for state in (before_protected, after_protected):
        require(isinstance(state.get("protected_rows_sha256"), str) and type(state.get("bad_derived_pins")) is int and type(state.get("physical_files_intact")) is bool, "normal_foreground_snapshot_types")
    require(all(re.fullmatch(r"[0-9a-f]{64}", state["protected_rows_sha256"]) is not None for state in (before_protected, after_protected)), "normal_foreground_snapshot_hash")
    expected_protection = (before_protected["protected_rows_sha256"] == after_protected["protected_rows_sha256"] and after_protected["bad_derived_pins"] == 0 and after_protected.get("bad_healthy_charges") == 0 and isinstance(after_protected.get("healthy_path_charges"), dict) and len(after_protected["healthy_path_charges"]) == size-size//100 and all(type(x) is int and x==16 for x in after_protected["healthy_path_charges"].values()) and after_protected["physical_files_intact"])
    require(foreground.get("protected_pass") is expected_protection, "normal_foreground_protection")
    expected_foreground = expected_protection and not errors and foreground.get("published_sources") == 80 and foreground.get("source_reservations_remaining") == 0 and foreground.get("counters_match_actual_ledger") is True
    require(foreground["pass"] == expected_foreground, "foreground_verdict")
    if expected_foreground:
        for key, count in (("reserve_source", 80), ("durable_source_publication", 80), ("retention_batch", 20), ("persisted_metadata_hit", 80)):
            latency(foreground.get(key), count)
    require(foreground.get("maintenance_budget_ms") == 10000 and foreground.get("api_and_metadata_budget_ms") == 5000 and foreground.get("monitor_budget_ms") == 1000 and foreground.get("monitor_errors_are_failures") is True, "normal_foreground_contexts")
    for name, budget, api in (("normal_maintenance", 10000, False), ("normal_api_bootstrap", 5000, True)):
        validate_normal_stage(stages[name], budget, api, size, identity["versions"][label]["schema"])
    failed = sorted(name for name, stage in stages.items() if not stage["pass"])
    require(isinstance(value.get("failures"), list) and sorted(value["failures"]) == failed and len(set(value["failures"])) == len(failed) and value["pass"] == (not failed), "whole_workload_verdict")
    correctness = all(stages[name]["pass"] for name in STAGES - {"normal_maintenance", "normal_api_bootstrap"})
    return {"verified_measurements": True, "workload_pass": value["pass"], "correctness_stages_pass": correctness, "failed_stages": failed, "size": size, "version": label, "performance_claim": False}


def validate_go_json(raw, exit_code, workload_pass):
    events = [loads(line) for line in raw.splitlines() if line.strip()]
    require(events and all(isinstance(e, dict) for e in events), "go_json_shape")
    require(all(e.get("Package") == "github.com/Kay0k1/cutmyvideo-core/internal/app" for e in events), "go_package")
    require(all("Test" not in e or e["Test"] == "TestStorageNormalCapacityPilot" for e in events), "go_test_scope")
    require(sum(e.get("Test") == "TestStorageNormalCapacityPilot" and e.get("Action") == "run" for e in events) == 1, "go_test_run")
    terminal = [e for e in events if e.get("Test") == "TestStorageNormalCapacityPilot" and e.get("Action") in ("pass", "fail", "skip")]
    require(len(terminal) == 1 and terminal[0]["Action"] == ("pass" if workload_pass else "fail") and exit_code == (0 if workload_pass else 1), "actual_go_terminal")
    require(not any(e.get("Action") == "skip" for e in events), "skipped_pilot")
    packages = [e for e in events if "Test" not in e and e.get("Action") in ("pass", "fail")]
    require(len(packages) == 1 and packages[0]["Action"] == terminal[0]["Action"], "go_package_terminal")
    return True


def validate_normal_stage(stage, budget, api, size, schema):
    """Recompute the distinct normal verdict; never consume a legacy5ms receipt."""
    require(isinstance(stage, dict) and type(stage.get("pass")) is bool and stage.get("profile") == "normal10s5s", "normal_stage_shape")
    require(stage.get("caller_budget_ms") == budget and stage.get("maximum_calls") == 8 and stage.get("fresh_independent_fixture") is True and stage.get("unbounded_repair_calls") == 0 and stage.get("service_SLA_claimed") is False, "normal_profile_bounds")
    require(stage.get("entrypoint") == ("ReserveSource_database_admission" if api else "RunMaintenance_one_shot") and stage.get("derived_pin_validation_applicable") is (schema == 5), "normal_entrypoint")
    paths, sizes = stage.get("expected_scan_paths"), stage.get("expected_scan_sizes")
    require(isinstance(paths, list) and len(paths) == size + 1024 and len(set(paths)) == len(paths) and all(isinstance(x, str) and x.startswith("/") and x.endswith("/normal-scan-%06d" % i) for i, x in enumerate(paths)), "normal_exact_paths")
    require(sizes == [9] * 1024 + [0] * size and stage.get("expected_scan_physical_files") == len(paths) and stage.get("expected_protected_physical_files") == size - size // 100, "normal_physical_fixture")
    healthy = stage.get("expected_healthy_paths")
    require(isinstance(healthy, list) and len(healthy)==size-size//100 and len(set(healthy))==len(healthy) and all(isinstance(p,str) and p.startswith("/") for p in healthy) and not set(healthy)&set(paths), "normal_exact_healthy_paths")
    expected = dict(zip(paths, sizes))
    fields = ("retained_sources", "retained_jobs", "healthy_artifacts", "preview_pins", "bad_derived_pins", "bad_path_charges", "bad_healthy_charges", "stored_bytes", "file_ledger_bytes", "reserved_bytes", "reservation_ledger_bytes", "initialization_markers", "completed_scan_kinds", "owned_api_reservation")
    def state(value):
        require(isinstance(value, dict) and all(type(value.get(k)) is int and value[k] >= 0 for k in fields) and type(value.get("physical_files_intact")) is bool and number(value.get("oracle_ms")), "normal_state_types")
        charges = value.get("committed_path_charges")
        require(isinstance(charges, dict) and set(charges) <= set(expected) and all(type(x) is int and x >= 0 for x in charges.values()), "normal_committed_charges")
        require(isinstance(value.get("protected_rows_sha256"), str), "normal_protected_rows")
        require(re.fullmatch(r"[0-9a-f]{64}",value["protected_rows_sha256"]) is not None, "normal_protected_snapshot_hash")
        healthy_charges = value.get("healthy_path_charges")
        require(isinstance(healthy_charges, dict) and set(healthy_charges)<=set(healthy) and all(type(x) is int and x>=0 for x in healthy_charges.values()), "normal_healthy_charge_shape")
        return charges
    initial, final = stage.get("initial"), stage.get("final")
    previous = state(initial)
    def preserved(value):
        return (value["protected_rows_sha256"] == initial["protected_rows_sha256"] and value["bad_derived_pins"] == 0 and value["bad_healthy_charges"] == 0 and value["healthy_path_charges"] == dict.fromkeys(healthy,16) and value["physical_files_intact"] and value["stored_bytes"] == value["file_ledger_bytes"] and value["reserved_bytes"] == value["reservation_ledger_bytes"] and value["bad_path_charges"] == 0 and all(value["committed_path_charges"][p] == expected[p] for p in value["committed_path_charges"]))
    valid = (initial["initialization_markers"] == 0 and previous == {} and initial["retained_sources"] == size and initial["retained_jobs"] == size and initial["healthy_artifacts"] == size - size // 100 and initial["preview_pins"] == 200 and preserved(initial))
    calls = stage.get("calls")
    require(isinstance(calls, list) and len(calls) <= 8 and all(isinstance(c, dict) for c in calls), "normal_finite_calls")
    complete = False
    for i, call in enumerate(calls, 1):
        require(not complete and call.get("call") == i and call.get("caller_budget_ms") == budget and number(call.get("effective_deadline_ms")) and 0 < call["effective_deadline_ms"] <= budget and number(call.get("wall_ms")), "normal_call_deadline")
        require(type(call.get("parent_expired")) is bool and type(call.get("acknowledged")) is bool and isinstance(call.get("error_type"), str), "normal_call_outcome_types")
        outcome = call.get("outcome")
        require(outcome in ("acknowledged", "late_acknowledged", "caller_deadline_pending", "initializing_pending", "genuine_error"), "normal_error_classification")
        require((outcome in ("acknowledged", "late_acknowledged")) is call["acknowledged"] and (outcome != "acknowledged" or not call["parent_expired"]) and (outcome not in ("late_acknowledged", "caller_deadline_pending") or call["parent_expired"]) and (outcome != "initializing_pending" or not call["parent_expired"]), "normal_attributable_deadline")
        observed = call.get("independent_reopened_state")
        charges = state(observed)
        monotonic = all(p in charges and charges[p] == x for p, x in previous.items())
        protected = preserved(observed)
        require(call.get("committed_progress_monotonic") is monotonic and call.get("protected_pass") is protected, "normal_recomputed_progress")
        valid = valid and monotonic and protected and outcome not in ("genuine_error", "late_acknowledged")
        complete = valid and call["acknowledged"] and not call["parent_expired"] and observed["initialization_markers"] == 1 and observed["completed_scan_kinds"] == (3 if api else 5) and charges == expected and (not api or observed["owned_api_reservation"] == 1)
        require(call.get("completed") is complete, "normal_acknowledged_completion")
        previous = charges
    state(final)
    require(all(p in final["committed_path_charges"] and final["committed_path_charges"][p] == x for p, x in previous.items()), "normal_final_durable_progress")
    require(type(stage.get("release_acknowledged")) is bool and type(stage.get("complete")) is bool and stage["complete"] is complete, "normal_complete_shape")
    passed = complete and valid and stage["release_acknowledged"] and preserved(final) and (not api or final["owned_api_reservation"] == 0)
    require(stage["pass"] is passed, "normal_recomputed_verdict")
    return passed
