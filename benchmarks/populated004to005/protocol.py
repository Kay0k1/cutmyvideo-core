"""Pure controls for the separately authorized populated migration experiment."""
import hashlib
import json
import re
from pathlib import Path

IDENTITY_SHA256 = "d7beba6f0231160788265f797601ae1b29c7d35977e8cdec210371a2c7a28ae8"
INSERT_QUERY = "INSERT INTO app_schema_migrations(position,version,checksum) VALUES($1,$2,$3)"
COUNTS = {"sources": 100000, "jobs": 100000, "artifacts": 25000,
          "source_metadata_cache": 100000, "storage_files": 125005,
          "storage_reservations": 125002, "storage_counters": 1,
          "storage_state": 1, "queue_owners": 1000,
          "storage_scan_progress": 3, "storage_work_cleanup": 2}
CALLS = ["success-upgrade", "success-restart", "deadline-blocked-upgrade",
         "deadline-labelled-recovery"]
SNAPSHOTS = [("success004-before", 4), ("success005-after-upgrade", 5),
             ("success005-after-restart", 5), ("deadline004-before", 4),
             ("deadline004-after-deadline", 4),
             ("deadline005-after-labelled-recovery", 5)]


class Refused(ValueError):
    pass


def require(condition, label):
    if not condition:
        raise Refused(label)


def canonical(value):
    return (json.dumps(value, sort_keys=True, indent=2, allow_nan=False) + "\n").encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def loads(raw):
    def unique(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result
    try:
        return json.loads(raw, object_pairs_hook=unique,
                          parse_constant=lambda _: (_ for _ in ()).throw(Refused("nonfinite JSON")))
    except (json.JSONDecodeError, UnicodeDecodeError):
        raise Refused("invalid JSON") from None


def digest(value, columns, order, count, xmin):
    require(isinstance(value, dict), "digest shape")
    require(type(value.get("rows")) is int and value["rows"] == count, "digest row count")
    require(type(value.get("utf8_bytes")) is int and value["utf8_bytes"] >= 0
            and type(value.get("framed_bytes")) is int
            and value["framed_bytes"] == value["utf8_bytes"] + 8 * count, "framed byte count")
    require(value.get("columns") == columns and value.get("order") == order
            and type(value.get("xmin")) is bool and value["xmin"] is xmin, "projection/order/xmin")
    require(re.fullmatch("[0-9a-f]{64}", value.get("sha256", "")), "digest SHA")
    require(value["utf8_bytes"] <= count * 32768, "row cap derived aggregate")
    if count == 0:
        require(value["utf8_bytes"] == 0 and value["sha256"] == sha(b""), "empty table actual digest")


def validate_identity(value):
    require(sha(canonical(value)) == IDENTITY_SHA256, "immutable populated identity")
    require(value["runtime"]["commit"] == "d594cc4681dc1cab61664cec8c92bdf9a3e9444c"
            and len(value["runtime"]["source_files"]) == 391
            and value["runtime"]["application_files"] == 173, "frozen runtime")
    require(value["runtime"]["application_selector"] == {
                "prefixes": ["cmd/", "internal/", "pkg/"], "exact": ["go.mod", "go.sum"]}
            and value["runtime"]["application_sha256"] ==
                "d1ae273e0d56212ef549f029970cd4ea1f33276faa7e4e11c88127e3aeeacef6"
            and value["caller_budgets_ns"] == {"api_database": 5000000000,
                "maintenance": 10000000000, "startup": 30000000000}, "current selector and unchanged budgets")
    require(all(v is None for v in value["actual_fields"].values())
            and value["full_raw_row_archive"] is False, "source-only identity")
    return value


def validate_snapshot(value, version, identity):
    require(value.get("version") == version and value.get("catalog_checked") is True, "snapshot schema")
    require(set(value.get("legacy004", {})) == set(identity["schema004"]), "old13 tables")
    require(set(value.get("full", {})) == set(identity["schema00" + str(version)]), "full schema tables")
    for table, projection in identity["schema004"].items():
        count = COUNTS.get(table, 4)
        digest(value["legacy004"][table], projection["columns"], projection["order"],
               count, table != "sources")
    for table, projection in identity["schema00" + str(version)].items():
        count = COUNTS.get(table, version)
        if table == "source_retention_audit":
            count = 1
        elif table == "source_pin_audit_progress":
            count = 0
        digest(value["full"][table], projection["columns"], projection["order"], count, True)
    digest(value["source_xmin"], ["id", "xmin"], ["id COLLATE C"], 100000, True)
    require(re.fullmatch("[0-9a-f]{64}", value.get("catalog_sha256", "")), "catalog digest")
    require(re.fullmatch("[0-9a-f]{64}", value.get("catalog_file_sha256", "")), "original catalog file digest")
    settings = value.get("settings", {})
    require(settings.get("TimeZone") == "UTC" and str(settings.get("DateStyle", "")).startswith("ISO")
            and settings.get("bytea_output") == "hex"
            and settings.get("isolation") == "repeatable read" and settings.get("access") == "on",
            "actual snapshot settings")
    ledger = value.get("ledger_original_rows")
    require(isinstance(ledger, list) and len(ledger) == version, "small original ledger")
    require(len(canonical(ledger)) <= 16384, "ledger bytes")
    require([r["row"]["position"] for r in ledger] == list(range(1, version + 1)), "ordered ledger")
    require(all(isinstance(r.get("xmin"), str) and r["xmin"].isdigit()
                and isinstance(r["row"].get("applied_at"), str)
                and re.fullmatch("[0-9a-f]{64}", r["row"].get("checksum", "")) for r in ledger), "original ledger values/xmin")
    if version == 5:
        require(ledger[4]["row"]["checksum"] == identity["migration005_lf_sha256"], "actual005 LF checksum")


def compare_snapshots(before, after, mode):
    require(before["legacy004"] == after["legacy004"], "retained004 rows/counts/xmin")
    if mode == "upgrade":
        require(before["ledger_original_rows"] == after["ledger_original_rows"][:4], "ledger1-4 retained")
    elif mode == "rollback":
        require(before["full"] == after["full"]
                and before["source_xmin"] == after["source_xmin"]
                and before["catalog_sha256"] == after["catalog_sha256"]
                and before["catalog_file_sha256"] == after["catalog_file_sha256"], "exact committed004 rollback")
    elif mode == "restart":
        require(before["full"] == after["full"]
                and before["source_xmin"] == after["source_xmin"]
                and before["catalog_sha256"] == after["catalog_sha256"]
                and before["catalog_file_sha256"] == after["catalog_file_sha256"]
                and before["ledger_original_rows"] == after["ledger_original_rows"], "restart performs no backfill")
    else:
        raise Refused("unknown comparison mode")


def validate_call(value, name):
    require(value.get("call") == name and value.get("caller") == "context.Background"
            and value.get("startup_budget_ns") == 30000000000
            and value.get("watchdog_ns") == 45000000000, "real OpenStore timer boundary")
    elapsed = value.get("elapsed_ns")
    require(type(elapsed) is int and elapsed >= 0, "actual monotonic elapsed")
    error = value.get("error", {})
    if name == "deadline-blocked-upgrade":
        require(value.get("nonnil_store") is False and error.get("deadline") is True
                and 29000000000 <= elapsed <= 32000000000
                and isinstance(error.get("message"), str) and error.get("chain"), "real deadline/nil store")
    else:
        require(value.get("nonnil_store") is True and error.get("deadline") is False
                and error.get("canceled") is False and error.get("chain") == []
                and "message" not in error and elapsed <= 30000000000, "success30s; failure cannot be replaced")


def validate_witness(value, schema, identity):
    observed = value.get("observed", {})
    require(value.get("pending_ordinal_source_derived") == 5
            and value.get("pre_call_exact_ledger_position") == 4
            and value.get("immutable005_checksum") == identity["migration005_lf_sha256"], "ordinal5 source/ledger binding")
    require(type(observed.get("startup_pid")) is int and type(observed.get("blocker_pid")) is int
            and observed["startup_pid"] > 0 and observed["blocker_pid"] > 0
            and observed["startup_pid"] != observed["blocker_pid"], "actual distinct PIDs")
    require(observed.get("query") == INSERT_QUERY
            and observed.get("startup_application") == schema + "_startup"
            and observed.get("startup_login") == schema + "_owner"
            and observed.get("relation") == '"' + schema + '"."app_schema_migrations"', "exact owned ledger INSERT")
    require(observed.get("waiting_mode") == "RowExclusiveLock" and observed.get("waiting_granted") is False
            and observed.get("blocker_mode") == "ShareLock" and observed.get("blocker_granted") is True
            and observed["blocker_pid"] in observed.get("blocking_pids", []), "observed blocking linkage")


def validate_migration_owner(value, schema):
    expected = {"current_user": schema + "_owner", "session_user": schema + "_owner",
                "role": schema + "_owner", "schema": schema, "login": True,
                "superuser": False, "create_database": False, "create_role": False,
                "inherit": False, "replication": False, "bypass_rls": False,
                "owns_schema": True, "schema_create": True, "database_create": False,
                "public_schema_create": False, "owned_schema_count": 1,
                "other_schema_create_count": 0, "membership_count": 0,
                "object_owner_mismatch_count": 0, "runtime_dml_least_privilege_checked": False}
    require(value == expected and all(type(value[key]) is type(wanted)
            for key, wanted in expected.items()), "actual migration-owner login/schema-only rights")


def validate_resource_sample(value):
    require(isinstance(value, dict) and isinstance(value.get("phase"), str), "resource sample phase")
    for key, maximum in (("volume_bytes", 2147483648), ("memory_current_bytes", 1073741824),
                         ("memory_peak_bytes", 1073741824), ("swap_current_bytes", 0)):
        require(type(value.get(key)) is int and 0 <= value[key] <= maximum, "actual resource cap " + key)
    require(value["memory_current_bytes"] <= value["memory_peak_bytes"], "actual cgroup peak")
    require(type(value.get("pg_disk_free_bytes")) is int and value["pg_disk_free_bytes"] >= 4294967296,
            "actual PG volume filesystem admission")
    events = value.get("memory_events", {})
    require(isinstance(events, dict) and all(type(n) is int and n >= 0 for n in events.values())
            and events.get("oom", -1) == events.get("oom_kill", -1) == 0, "actual OOM evidence")


def validate_oracles(value, version):
    counts = dict(COUNTS, app_schema_migrations=version, app_schema_versions=version)
    if version == 5:
        counts.update(source_retention_audit=1, source_pin_audit_progress=0)
    require(value.get("counts") == counts and all(type(v) is int for v in value["counts"].values())
            and type(value.get("stored_bytes")) is int and type(value.get("reserved_bytes")) is int
            and value.get("stored_bytes") == 2000065
            and value.get("reserved_bytes") == 2025018 and value.get("actual_accounting_equal") is True, "actual seeded counts/ledger")
    if version == 5:
        jobs, previews = [0, 1, 1, 1, 1, 1, 1, 2], [0, 0, 1, 2, 0, 1, 0, 0]
        expected = [[g, 12500, 12500 * jobs[g], 12500 * previews[g]] for g in range(8)]
        require(value.get("groups") == expected
                and all(type(n) is int for group in value["groups"] for n in group)
                and all(type(value.get(k)) is int for k in ("pin_mismatch_count", "eligible_sources", "job_pins", "preview_pins"))
                and value.get("pin_mismatch_count") == 0
                and value.get("eligible_sources") == 12500
                and value.get("job_pins") == 100000 and value.get("preview_pins") == 50000, "all groups/terminal jobs/exact preview pins")


def validate_fixture(directory, identity):
    directory = Path(directory)
    def read(name, maximum=2 * 1024 * 1024):
        path = directory / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= maximum, "bounded actual evidence " + name)
        return loads(path.read_bytes())
    final = read("fixture-final.json", 16384)
    require(final.get("OpenStore_calls") == final.get("maximum_calls") == 4
            and final.get("failed") is False and final.get("complete") is True
            and type(final.get("remaining_roles")) is int and final["remaining_roles"] == 0
            and final.get("remaining_schemas") == 0 and final.get("full_raw_row_archive") is False, "complete four calls and cleanup")
    schemas = final.get("owned_schemas")
    require(isinstance(schemas, list) and len(schemas) == 2
            and re.fullmatch("populated100k_success_[0-9a-f]{32}", schemas[0])
            and re.fullmatch("populated100k_deadline_[0-9a-f]{32}", schemas[1]), "two sequential owned schemas")
    require(final.get("owned_roles") == [schema + "_owner" for schema in schemas], "two owned migration roles")
    for name in CALLS:
        validate_call(read(name + "-open.json", 16384), name)
    snapshots = {}
    for name, version in SNAPSHOTS:
        snapshots[name] = read(name + "-snapshot.json")
        validate_snapshot(snapshots[name], version, identity)
        read(name + "-catalog.json", 1048576)
        require(sha((directory / (name + "-catalog.json")).read_bytes()) == snapshots[name]["catalog_file_sha256"],
                "original catalog bytes bound to snapshot")
    for old, new, mode in [
            ("success004-before", "success005-after-upgrade", "upgrade"),
            ("success005-after-upgrade", "success005-after-restart", "restart"),
            ("deadline004-before", "deadline004-after-deadline", "rollback"),
            ("deadline004-before", "deadline005-after-labelled-recovery", "upgrade")]:
        compare_snapshots(snapshots[old], snapshots[new], mode)
    for name, version in [("success004", 4), ("success005-after-upgrade", 5),
                          ("deadline004", 4), ("deadline004-after-deadline", 4),
                          ("deadline005-after-labelled-recovery", 5)]:
        validate_oracles(read(name + "-oracles.json"), version)
    validate_witness(read("deadline-ledger-insert-lock-witness.json", 16384), schemas[1], identity)
    for name, schema in (("success004", schemas[0]), ("success005-after-upgrade", schemas[0]),
                         ("success005-after-restart", schemas[0]), ("deadline004", schemas[1]),
                         ("deadline005-after-labelled-recovery", schemas[1])):
        validate_migration_owner(read(name + "-migration-owner.json", 16384), schema)
    for which, schema in zip(("success", "deadline"), schemas):
        require(read(which + "-namespace-cleanup.json", 16384) == {"schema": schema, "remaining": 0,
                "role": schema + "_owner", "remaining_roles": 0}, "sequential namespace/role absence")
    return {"evidence_verified": True, "OpenStore_calls": 4, "snapshots": 6,
            "populated_rows": 100000, "full_raw_row_archive": False,
            "startup_capacity_sample_count": 1, "restart_and_deadline_recovery_are_controls": True,
            "migration_owner_permissions_checked": True, "runtime_dml_least_privilege_checked": False}
