"""Offline verifier for a future once-collected hosted artifact and context."""
from __future__ import annotations

import argparse
from pathlib import Path, PurePosixPath
import re

from protocol import (AFTER, BEFORE, IDENTITY_SHA256, PG_CHILD, PG_CONFIG, PG_INDEX,
                      canonical, loads, number, require, sha, validate_go_json,
                      validate_identity, validate_image, validate_report)


def verify_catalog(directory, catalog):
    require(isinstance(catalog, dict) and 0 < len(catalog) <= 300, "artifact_catalog")
    all_paths = list(directory.rglob("*"))
    require(not any(p.is_symlink() for p in all_paths), "artifact_symlink")
    total = 0
    for path, entry in catalog.items():
        require(isinstance(path, str) and isinstance(entry, dict) and type(entry.get("bytes")) is int and 0 <= entry["bytes"] <= 64 * 1024 * 1024, "artifact_entry")
        total += entry["bytes"]
        require(total <= 256 * 1024 * 1024, "artifact_total_size")
        parsed = PurePosixPath(path)
        require(not parsed.is_absolute() and parsed.as_posix() == path and ".." not in parsed.parts and path != "summary.json", "artifact_path")
        target = directory.joinpath(*parsed.parts)
        require(target.is_file() and not target.is_symlink(), "artifact_file")
        require(target.stat().st_size == entry["bytes"], "original_file_size")
        raw = target.read_bytes()
        require(entry == {"bytes": len(raw), "sha256": sha(raw)}, "original_file_hash")
    actual = {p.relative_to(directory).as_posix() for p in all_paths if p.is_file()}
    require(actual == set(catalog) | {"summary.json"}, "unexpected_artifact_file")
    return len(catalog)


def verify(directory, context, identity):
    validate_identity(identity)
    require(isinstance(context, dict) and context.get("format") == 1 and context.get("repository") == "Kay0k1/cutmyvideo-core" and context.get("event") == "workflow_dispatch" and context.get("attempt") == 1, "actual_context")
    require(type(context.get("run_id")) is int and context["run_id"] > 0 and re.fullmatch(r"[0-9a-f]{40}", context.get("reviewed_harness_commit", "")) and re.fullmatch(r"[0-9a-f]{64}", context.get("preparation_review_sha256", "")), "reviewed_dispatch")
    require(context.get("runtime_commits") == {"before": BEFORE, "after": AFTER} and re.fullmatch(r"[0-9a-f]{64}", context.get("fixture_sha256", "")), "reviewed_fixture_context")
    summary = loads((directory / "summary.json").read_bytes())
    require(isinstance(summary, dict) and summary.get("format") == 1 and summary.get("kind") == "storage005-fixed-hosted-pilot" and summary.get("complete") is True and type(summary.get("pass")) is bool and summary.get("failure_code") is None and summary.get("container_cleanup") is True, "completed_pilot")
    dispatch = summary.get("dispatch", {})
    require(dispatch == {"GITHUB_SHA": context["reviewed_harness_commit"], "GITHUB_RUN_ID": str(context["run_id"]), "GITHUB_RUN_ATTEMPT": "1", "GITHUB_WORKFLOW_REF": context["workflow_ref"], "GITHUB_REPOSITORY": context["repository"]}, "provider_dispatch_binding")
    require(summary.get("identity_sha256") == IDENTITY_SHA256 and summary.get("fixture_sha256") == context["fixture_sha256"] and summary.get("go_version") == "go version go1.27.2 linux/amd64", "harness_fixture_toolchain")
    catalog = summary.get("catalog")
    verify_catalog(directory, catalog)
    versions = summary.get("versions", {})
    require(set(versions) == {"before", "after"}, "compiled_sources")
    for label in ("before", "after"):
        expected = identity["versions"][label]
        for phase in ("before-injection", "after-compile", label + "-1000-after", label + "-10000-after"):
            prefix = label + "-" + phase
            observed = loads((directory / (prefix + "-source-map.json")).read_bytes())
            require(observed == {"application_files": expected["application_files"], "injected_fixture_sha256": None if phase == "before-injection" else context["fixture_sha256"]}, "canonical_tracked_source_map")
            for field, suffix in (("commit", "head"), ("tree", "tree")):
                require((directory / (prefix + "-" + suffix + ".stdout")).read_text().strip() == expected[field], "raw_git_identity")
            require((directory / (prefix + "-parents.stdout")).read_text().strip().split() == expected["parents"], "raw_git_parents")
            expected_status = "" if phase == "before-injection" else "?? internal/app/storage005_comparison_pilot_test.go\n"
            require((directory / (prefix + "-status.stdout")).read_text() == expected_status, "raw_worktree_identity")
    workloads = summary.get("workloads")
    require(isinstance(workloads, list) and [(w.get("size"), w.get("version")) for w in workloads] == [(1000, "before"), (1000, "after"), (10000, "before"), (10000, "after")], "ordered_pilot_pairs")
    for filename, digest in (("registry-index.raw.json", PG_INDEX), ("registry-amd64.raw.json", PG_CHILD)):
        require("sha256:" + sha((directory / filename).read_bytes()) == digest, "actual_registry_bytes")
    require(loads((directory / "registry-amd64.raw.json").read_bytes()).get("config", {}).get("digest") == PG_CONFIG, "actual_registry_config")
    commands = summary.get("commands")
    require(isinstance(commands, list) and commands and all(isinstance(c, dict) for c in commands), "command_receipts")
    by_stdout = {}
    for command in commands:
        require(isinstance(command.get("command"), list) and command["command"] and all(isinstance(x, str) for x in command["command"]), "actual_command_argv")
        require(type(command.get("exit_code")) is int and command.get("timeout") is False and command.get("overflow") is False and command.get("stream_errors") == [] and number(command.get("elapsed_ms")), "actual_command_outcome")
        require(command.get("stdout") in catalog and command.get("stderr") in catalog and command["stdout"] not in by_stdout, "actual_command_files")
        by_stdout[command["stdout"]] = command
        if command["exit_code"] != 0:
            is_test = "test2json" in command["command"] and command["stdout"] in {l + "-" + str(n) + "/go-events.stdout" for n in (1000, 10000) for l in ("before", "after")}
            is_poll = len(command["command"]) > 3 and command["command"][:2] == ["docker", "exec"] and command["command"][3] == "pg_isready"
            require((is_test and command["exit_code"] == 1) or (is_poll and command["exit_code"] in (1, 2)), "required_command_failure")
    for label in ("before", "after"):
        compile_receipt = by_stdout.get(label + "-compile.stdout", {})
        argv = compile_receipt.get("command", [])
        require(len(argv) == 6 and argv[:4] == ["go", "test", "-c", "-o"] and Path(argv[4]).name == label + ".test" and argv[5] == "./internal/app" and compile_receipt.get("exit_code") == 0, "actual_remote_compile")
    verified = []
    for work in workloads:
        label, size = work["version"], work["size"]
        prefix = label + "-" + str(size)
        require(work.get("report") == prefix + "/pilot.json" and work.get("go_json") == prefix + "/go-events.stdout", "workload_paths")
        report_raw = (directory / work["report"]).read_bytes()
        go_raw = (directory / work["go_json"]).read_bytes()
        require(sha(report_raw) == work.get("report_sha256") and sha(go_raw) == work.get("go_json_sha256"), "workload_original_hashes")
        verdict = validate_report(loads(report_raw), label, size, identity)
        validate_go_json(go_raw, work.get("go_exit"), verdict["workload_pass"])
        command = by_stdout.get(prefix + "/go-events.stdout", {})
        argv = command.get("command", [])
        require(len(argv) == 15 and argv[:3] == ["/usr/bin/time", "-v", "-o"] and argv[4:10] == ["go", "tool", "test2json", "-t", "-p", "github.com/Kay0k1/cutmyvideo-core/internal/app"] and Path(argv[10]).name == label + ".test" and argv[11:] == ["-test.run=^TestStorage005ComparisonPilot$", "-test.count=1", "-test.timeout=6m", "-test.v=test2json"] and command.get("exit_code") == work["go_exit"], "actual_timed_test_command")
        require(verdict == work.get("verification") and work.get("namespace_role_cleanup") is True and (directory / (prefix + "-namespace-cleanup.stdout")).read_bytes().strip() == b"0|0", "workload_cleanup_verdict")
        require(work.get("image") == workloads[0].get("image") and work.get("postgresql_settings") == workloads[0].get("postgresql_settings"), "identical_postgres")
        for suffix in ("", "-after"):
            image = validate_image((directory / (prefix + suffix + "-container.stdout")).read_bytes(), (directory / (prefix + suffix + "-image.stdout")).read_bytes())
            require(image == work["image"], "raw_same_postgres_identity")
        require(number_positive(work.get("postgres_cgroup_cumulative_peak_bytes")) and (directory / (prefix + "-postgres-memory-peak.stdout")).read_bytes().strip() == str(work["postgres_cgroup_cumulative_peak_bytes"]).encode(), "actual_memory_peak")
        usage = (directory / (prefix + "/process-usage.txt")).read_text()
        require(re.search(r"Maximum resident set size \(kbytes\): [1-9][0-9]*", usage), "actual_process_resource_receipt")
        verified.append(verdict)
    require(all(w["correctness_stages_pass"] for w in verified[:2]), "10000_pilot_prerequisite")
    require(summary["pass"] == all(w["workload_pass"] for w in verified), "whole_campaign_verdict")
    require((directory / "container-remaining.stdout").read_bytes().strip() == b"", "raw_container_absence")
    admissions = summary.get("resource_admissions", [])
    require(len(admissions) == 8 and all(a.get("disk_free_bytes", 0) >= 2 * 1024**3 and a.get("mem_available_bytes", 0) >= 3 * 1024**3 and a.get("host_cpus", 0) >= 2 for a in admissions), "actual_stage_admissions")
    return {"format": 1, "measurement_evidence_verified": True, "workload_pass": summary["pass"], "failed_workloads": [w for w in verified if not w["workload_pass"]], "source_inventory_files": {"before": 164, "after": 168}, "pairs": [1000, 10000], "summary_sha256": sha((directory / "summary.json").read_bytes()), "context_sha256": sha(canonical(context)), "performance_claim": False, "100000_authorized": False}


def number_positive(value):
    return type(value) is int and value > 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--context", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    proof = verify(args.directory, loads(args.context.read_bytes()), loads(Path(__file__).with_name("identity.json").read_bytes()))
    with args.output.open("xb") as target:
        target.write(canonical(proof))
    print("measurement evidence verified; workload_pass=" + str(proof["workload_pass"]).lower())
    return 0 if proof["workload_pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
