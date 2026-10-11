"""Offline verification of this one current10k diagnostic; no runtime repair."""
import argparse
from collections import Counter
from pathlib import Path
import json
import re
import run_hosted as runner
from observe import decode, parse_memory, validate_marker, validate_sample, MAX_BYTES, MAX_SAMPLES


def verify(directory):
    require, sha, loads = runner.require, runner.sha, runner.loads
    root=directory
    require(root.is_dir() and not root.is_symlink(), "owned_evidence_root")
    root_catalog=loads((root/"hosted-catalog.json").read_bytes())
    require(isinstance(root_catalog,dict) and len(root_catalog) <= 1500, "hosted_catalog_shape")
    root_actual={p.relative_to(root).as_posix() for p in root.rglob("*") if p.is_file()}
    require(root_actual == set(root_catalog) | {"hosted-catalog.json"}, "hosted_fileset")
    root_total=0
    for name,record in root_catalog.items():
        relative=Path(name)
        require(not relative.is_absolute() and ".." not in relative.parts, "hosted_catalog_path")
        path=root/relative
        require(path.is_file() and not path.is_symlink() and all(not parent.is_symlink() for parent in path.parents if parent != root.parent), "hosted_file_type")
        raw=path.read_bytes();root_total+=len(raw)
        require(root_total <= 256*1024*1024 and record == {"bytes":len(raw),"sha256":sha(raw)}, "hosted_catalog_bytes")
    outer=loads((root/"hosted-result.json").read_bytes())
    always=loads((root/"always-cleanup.json").read_bytes())
    original=outer.get("once_result")
    require(isinstance(original,dict) and original.get("exit_code") == 0 and original.get("reaped") is True
            and original.get("owned_groups_absent") is True and original.get("remaining_owned_processes") == {}
            and original.get("elapsed_seconds") <= original.get("allocated_wall_seconds")
            and not any(original.get(k) for k in ("timeout","cancelled","forced","overflow","stream_errors")), "outer_owned_capture")
    for label in ("stdout","stderr"):
        raw=(root/("outer-once."+label+".bin")).read_bytes()
        require(len(raw) == original[label+"_bytes"] <= 50331648 and sha(raw) == original[label+"_sha256"], "outer_original_stream")
    require(outer.get("pass") is True and outer.get("cancelled") is False
            and outer.get("runtime_commit") == runner.PINS["runtime"]["commit"]
            and outer.get("elapsed_seconds") <= outer.get("allocated_wall_seconds") == 1800
            and outer.get("cleanup",{}).get("pass") is True and always.get("pass") is True
            and always.get("elapsed_within_deadline") is True
            and always.get("process_reconciliation",{}).get("remaining") == {}, "supervised_terminal_cleanup")
    directory=root/"once"
    summary = loads((directory / "summary.json").read_bytes())
    context=loads((root/"context.json").read_bytes())
    dispatch=summary.get("dispatch",{})
    require(context.get("attempt") == 1 and dispatch.get("GITHUB_RUN_ATTEMPT") == "1"
            and context.get("run_id") == dispatch.get("GITHUB_RUN_ID")
            and isinstance(context.get("run_id"),str) and context["run_id"].isdigit()
            and re.fullmatch("[0-9a-f]{40}",context.get("tooling_commit",""))
            and context["tooling_commit"] == dispatch.get("GITHUB_SHA")
            and (root/"tooling-head.stdout.bin").read_bytes().decode().strip() == context["tooling_commit"]
            and dispatch.get("GITHUB_REPOSITORY") == "Kay0k1/cutmyvideo-core"
            and context.get("wrapper_sha256") == sha((runner.HERE/"run_supervised.py").read_bytes())
            and context.get("fixture_sha256") == summary.get("fixture_sha256")
            and context.get("fixture_preexisting") is False and context.get("private_preexisting") is False,
            "reviewed_attempt_source_context")
    require(summary.get("kind") == "storage-normal-memory-current10000" and summary.get("maximum_size") == 10000 and summary.get("diagnostic_only") is True and summary.get("promotion_ready") is False, "diagnostic_identity")
    require(summary.get("identity_sha256") == sha((runner.HERE/"identity.json").read_bytes()) and summary.get("fixture_sha256") == sha((runner.HERE/"pilot_test.go.txt").read_bytes()), "diagnostic_source_hash")
    catalog = summary.get("catalog")
    require(isinstance(catalog, dict) and len(catalog) <= 1200, "diagnostic_catalog")
    actual = {p.relative_to(directory).as_posix() for p in directory.rglob("*") if p.is_file()}
    require(actual == set(catalog) | {"summary.json"}, "diagnostic_fileset")
    total = 0
    for name, record in catalog.items():
        relative = Path(name)
        require(not relative.is_absolute() and ".." not in relative.parts and name != "summary.json", "catalog_path")
        path = directory / relative
        require(path.is_file() and not path.is_symlink() and all(not parent.is_symlink() for parent in path.parents if parent != directory.parent), "catalog_file_type")
        raw = path.read_bytes();total += len(raw)
        require(total <= 256*1024*1024 and record == {"bytes":len(raw),"sha256":sha(raw)}, "catalog_bytes")
    works = summary.get("workloads")
    require(isinstance(works,list) and len(works) == 1 and works[0].get("version") == "after" and works[0].get("size") == 10000, "only_current10000")
    work = works[0]
    require(work.get("report") == "after-10000/pilot.json" and work.get("go_json") == "after-10000/go-events.stdout", "fixed_workload_paths")
    report_raw = (directory/work["report"]).read_bytes(); report = loads(report_raw)
    legacy_identity = runner.validate_identity(loads((runner.WORKSPACE/"benchmarks/storage-normal-capacity/identity.json").read_bytes()))
    verdict = runner.validate_report(report,"after",10000,legacy_identity)
    require(verdict == work["verification"] and verdict["workload_pass"] is True and sha(report_raw) == work["report_sha256"], "all_seven_oracles")
    go_raw = (directory/work["go_json"]).read_bytes()
    require(sha(go_raw) == work["go_json_sha256"], "go_raw_hash")
    runner.validate_go_json(go_raw,work["go_exit"],True)
    require(summary.get("container_cleanup") is True and (directory/"container-remaining.stdout").read_bytes().strip() == b"" and (directory/"volume-remaining.stdout").read_bytes().strip() == b"" and (directory/"after-10000-namespace-cleanup.stdout").read_bytes().strip() == b"0|0", "owned_cleanup")
    for command in summary["commands"]:
        require(command.get("exit_code") == 0 or command["stdout"].startswith("tcp-ready-"), "command_failure")
        require(command.get("reaped") is True and command.get("interrupted") is False and command.get("refused") is False, "command_owned_capture")
        require(command.get("timeout") is False and command.get("overflow") is False and command.get("stream_errors") == [], "command_capture_failure")
    stage = directory/"after-10000"
    sample_raw = (stage/"memory-samples.jsonl").read_bytes()
    require(len(sample_raw) <= MAX_BYTES, "memory_stream_cap")
    previous = None;count = 0;first = None
    for line in sample_raw.splitlines():
        require(len(line) <= 65536 and count < MAX_SAMPLES, "memory_line_cap")
        sample = decode(line);memory = validate_sample(previous,sample)
        previous = sample;count += 1
        if first is None: first = memory
    require(count > 1, "memory_samples_missing")
    final = summary.get("memory_observer")
    require(isinstance(final,dict) and final.get("pass") is True and final.get("threads_joined") is True and final.get("failure") is None and final.get("samples") == count and final.get("bytes") == len(sample_raw) and final.get("first") == first and final.get("last") == memory and final.get("oom_counter_increased") is False, "observer_raw_binding")
    require(first is not None and all(memory["memory.events"][k] == first["memory.events"][k] for k in ("oom","oom_kill")), "oom_counter_delta")
    stack=[];phases=[];last_elapsed=-1
    for sequence in range(1, final["marker_count"]+1):
        marker=decode((stage/"markers"/("%03d.request.json"%sequence)).read_bytes())
        require(marker["elapsed_ns"] > last_elapsed, "marker_clock_order");last_elapsed=marker["elapsed_ns"]
        stack=validate_marker(marker,sequence,stack)
        ack=decode((stage/"markers"/("%03d.ack.json"%sequence)).read_bytes())
        require(ack.get("sequence") == sequence and ack.get("pass") is True and ack.get("scope") == stack and type(ack.get("observer_boundary_ns")) is int and 0 <= ack["observer_boundary_ns"] <= runner.PINS["diagnostic_boundary_ack_seconds"] * 1_000_000_000, "boundary_ack")
        parse_memory(ack["memory_begin_raw"]);parse_memory(ack["memory_end_raw"])
        stem="boundary-%03d"%sequence
        sql=(directory/(stem+"-sql.stdout")).read_bytes();disk=(directory/(stem+"-disk.stdout")).read_bytes()
        require(ack["snapshot"]["sql_sha256"] == sha(sql) and ack["snapshot"]["disk_sha256"] == sha(disk), "boundary_raw_binding")
        runner.validate_boundary(sql,disk)
        phases.append(marker["phase"])
    calls=sum(len(report["stages"][key]["calls"]) for key in ("normal_maintenance","normal_api_bootstrap"))
    expected={"workload_setup":1,"report_serialization":1,"all_fixture_cleanup":1,"normal_fixture_setup":2,"comparison_oracle":5,"normal_oracle":6+calls,"product_call":calls}
    expected.update({"stage_"+key:1 for key in report["stages"]})
    require(not stack and Counter(phases) == Counter({base+"_"+edge:n for base,n in expected.items() for edge in ("begin","end")}), "all_phase_counts")
    require(phases[-1] == "all_fixture_cleanup_end" and final.get("markers_complete") is True and final.get("marker_count") == len(phases), "cleanup_phase_end")
    require(summary.get("pass") is True and summary.get("complete") is True, "diagnostic_terminal")
    return {"format":1,"kind":"storage-normal-memory-readonly-verification","pass":True,"current_runtime":runner.PINS["runtime"]["commit"],"samples":count,"markers":len(phases),"cumulative_peak_bytes":memory["memory.peak"],"performance_or_memory_headroom_claim":False,"summary_sha256":sha((directory/"summary.json").read_bytes())}


def main():
    parser=argparse.ArgumentParser();parser.add_argument("--directory",type=Path,required=True);parser.add_argument("--output",type=Path,required=True);args=parser.parse_args()
    result=verify(args.directory.resolve())
    runner.exclusive(args.output,runner.canonical(result))
    print(json.dumps(result,sort_keys=True))


if __name__ == "__main__": main()
