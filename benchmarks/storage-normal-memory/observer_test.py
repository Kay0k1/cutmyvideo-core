"""Written pure controls: fake cgroup bytes and mocks, never real workloads."""
import copy
import io
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import unittest
from unittest import mock
import urllib.request

ATTEMPTS = 0
def forbidden(*args, **kwargs):
    global ATTEMPTS
    ATTEMPTS += 1
    raise AssertionError("actual child/network forbidden")

GUARDS = [mock.patch.object(subprocess,"Popen",forbidden),mock.patch.object(urllib.request,"urlopen",forbidden),mock.patch.object(socket.socket,"connect",forbidden),mock.patch.object(socket.socket,"connect_ex",forbidden),mock.patch.object(socket,"create_connection",forbidden),mock.patch.object(os,"system",forbidden)]
for name in ("posix_spawn","posix_spawnp"):
    if hasattr(os,name): GUARDS.append(mock.patch.object(os,name,forbidden))
for guard in GUARDS: guard.start()
from observe import (bind_cgroup, decode, parse_memory, publish, read, validate_marker, validate_sample)

def sample(sequence=1, clock=1_000_000_000, peak=200, current=100):
    raw = {"memory.current":str(current),"memory.peak":str(peak),"memory.max":"1073741824","memory.stat":"anon 50\nfile 40\nkernel 10\nshmem 4\n","memory.events":"max 0\noom 0\noom_kill 0\n","memory.events.local":"max 0\noom 0\noom_kill 0\n","memory.swap.current":"0","memory.swap.max":"0","memory.swap.events":"high 0\nmax 0\nfail 0\n","memory.swap.peak":None}
    return {"sequence":sequence,"monotonic_ns":clock,"raw":raw}

class MemoryObserverControls(unittest.TestCase):
    @classmethod
    def tearDownClass(cls):
        try:
            if ATTEMPTS: raise AssertionError("actual child/network attempt")
            print("MEMORY PURE CONTROL actual_child_network_attempts=0",flush=True)
        finally:
            for guard in reversed(GUARDS): guard.stop()

    def test_cumulative_peak_can_stay_high_while_current_falls(self):
        old=sample();new=sample(2,1_100_000_000,current=50)
        self.assertEqual(validate_sample(old,new)["memory.current"],50)
        self.assertEqual(parse_memory(new["raw"])["memory.peak"],200)

    def test_missing_key_duplicate_negative_and_wrong_limit_refuse(self):
        cases=[]
        d=sample()["raw"];del d["memory.current"];cases.append(d)
        d=sample()["raw"];d["memory.events"]+="oom 1\n";cases.append(d)
        d=sample()["raw"];d["memory.stat"]="anon -1\n";cases.append(d)
        d=sample()["raw"];d["memory.swap.max"]="max";cases.append(d)
        for i,d in enumerate(cases):
            with self.subTest(case=i),self.assertRaises(ValueError):parse_memory(d)

    def test_missed_tick_backwards_clock_and_peak_refuse(self):
        for seq,clock,peak in ((3,1_100_000_000,200),(2,1_201_000_000,200),(2,999_000_000,200),(2,1_100_000_000,199)):
            with self.subTest(seq=seq,clock=clock,peak=peak),self.assertRaises(ValueError):validate_sample(sample(),sample(seq,clock,peak))

    def test_oom_and_pressure_counters_preserved_not_hidden(self):
        new=sample(2,1_100_000_000);new["raw"]["memory.events"]="max 2\noom 1\noom_kill 1\n"
        self.assertEqual(validate_sample(sample(),new)["memory.events"],{"max":2,"oom":1,"oom_kill":1})
        with self.assertRaises(ValueError):validate_sample(new,sample(3,1_200_000_000))

    def test_exact_container_binding_rejects_foreign_pid_and_traversal(self):
        id="a"*64
        for cgroup in ("0::/docker/"+"b"*64+"\n","0::/docker/../"+id+"\n"):
            with mock.patch("observe.proc_identity",return_value={"start_ticks":1,"cgroup_raw":cgroup}),self.assertRaises(ValueError):bind_cgroup((id+" 42\n").encode())
        for raw in (b"bad 42\n",(id+" 0\n").encode()):
            with self.assertRaises(ValueError):bind_cgroup(raw)

    def test_owned_pid_reuse_or_cgroup_change_refuses(self):
        from observe import Observer
        binding={"pid":42,"start_ticks":1,"cgroup_raw":"0::/owned\n"}
        o=Observer(binding,Path("/unused"),Path("/unused"),None)
        for identity in ({"start_ticks":2,"cgroup_raw":"0::/owned\n"},{"start_ticks":1,"cgroup_raw":"0::/foreign\n"}):
            with mock.patch("observe.proc_identity",return_value=identity),self.assertRaises(ValueError):o.check_owned()

    def test_atomic_ack_is_exclusive_and_input_is_bounded(self):
        with tempfile.TemporaryDirectory() as temporary:
            p=Path(temporary)/"ack.json";publish(p,{"sequence":1,"pass":True})
            self.assertEqual(decode(read(p)),{"sequence":1,"pass":True})
            with self.assertRaises(FileExistsError):publish(p,{"sequence":1,"pass":False})
            with self.assertRaises(ValueError):read(p,2)

    def test_product_marker_must_close_before_oracle_or_stage_end(self):
        m=lambda n,p:{"sequence":n,"phase":p,"utc":"2026-10-11T00:00:00Z","elapsed_ns":n}
        stack=validate_marker(m(1,"stage_normal_maintenance_begin"),1,[])
        stack=validate_marker(m(2,"product_call_begin"),2,stack)
        with self.assertRaises(ValueError):validate_marker(m(3,"normal_oracle_begin"),3,stack)
        with self.assertRaises(ValueError):validate_marker(m(3,"stage_normal_maintenance_end"),3,stack)
        stack=validate_marker(m(3,"product_call_end"),3,stack)
        stack=validate_marker(m(4,"normal_oracle_begin"),4,stack)
        stack=validate_marker(m(5,"normal_oracle_end"),5,stack)
        self.assertEqual(validate_marker(m(6,"stage_normal_maintenance_end"),6,stack),[])

    def test_wrong_marker_identity_unknown_phase_and_duplicate_json_refuse(self):
        good={"sequence":1,"phase":"normal_oracle_begin","utc":"2026-10-11T00:00:00Z","elapsed_ns":1}
        for field,value in (("sequence",2),("phase","vacuum_begin"),("elapsed_ns",-1)):
            d=copy.deepcopy(good);d[field]=value
            with self.subTest(field=field),self.assertRaises(ValueError):validate_marker(d,1,[])
        with self.assertRaises(ValueError):decode('{"sequence":1,"sequence":2}')

    def test_oom_gap_missing_phases_and_unjoined_observer_cannot_pass(self):
        from observe import Observer
        with tempfile.TemporaryDirectory() as temporary:
            p=Path(temporary);o=Observer({},p,p,None)
            self.assertFalse(o.result()["pass"])
            for error in ("sample_time_gap","sample_output_cap","observer_thread_not_joined","boundary_failed"):
                o.failure=error
                self.assertFalse(o.result()["pass"])
            o.failure=None
            thread=mock.Mock();thread.is_alive.return_value=True;o.threads=[thread]
            o.stop()
            thread.join.assert_called_once()
            self.assertGreater(thread.join.call_args.args[0],0)
            self.assertLessEqual(thread.join.call_args.args[0],50)
            self.assertEqual(o.failure,"observer_thread_not_joined")
            self.assertFalse(o.result()["threads_joined"])
            self.assertFalse(o.result()["pass"])

    def test_runner_and_saved_reader_imports_are_inert(self):
        import run_hosted
        import verify_saved
        self.assertEqual(ATTEMPTS,0)
        self.assertEqual(run_hosted.PINS["sizes"],[10000])

    def test_output_cap_refuses_without_silently_dropping_a_good_sample(self):
        from observe import Observer
        with tempfile.TemporaryDirectory() as temporary:
            p=Path(temporary);o=Observer({},p,p,None)
            with mock.patch.object(o,"raw_memory",return_value=sample()["raw"]),mock.patch("observe.MAX_BYTES",1):
                o.sample_loop()
            self.assertEqual(o.failure,"sample_output_cap")
            self.assertEqual(o.samples,0)
            self.assertEqual((p/"memory-samples.jsonl").read_bytes(),b"")
            self.assertFalse(o.result()["pass"])

    def test_boundary_read_errors_and_pipe_failure_are_not_good_snapshots(self):
        import run_hosted
        good=b'{"data_directory":"/var/lib/postgresql/data","database_bytes":1,"database_stats":{},"owned_tables":[]}'
        disk=b'1 /var/lib/postgresql/data\n1 /var/lib/postgresql/data/pg_wal\ntemporary_files 0 temporary_bytes 0\n'
        self.assertEqual(run_hosted.validate_boundary(good,disk)["database_bytes"],1)
        for raw in (b'',b'1 wrong\n1 wrong\ntemporary_files 0 temporary_bytes 0\n',disk.replace(b'bytes 0',b'bytes invalid')):
            with self.subTest(raw=raw),self.assertRaises(ValueError):run_hosted.validate_boundary(good,raw)
        with mock.patch.object(run_hosted,"checked",side_effect=ValueError("fixed_command_failed")):
            with self.assertRaises(ValueError):run_hosted.boundary("owned",Path("/unused"),{"sequence":1})


    def test_sticky_refusal_blocks_spawn_but_cleanup_has_a_bounded_override(self):
        import run_hosted
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);output=root/"once";output.mkdir()
            old=run_hosted.ARTIFACT_ROOT;run_hosted.ARTIFACT_ROOT=output
            run_hosted.REFUSED.set()
            try:
                with mock.patch.object(subprocess,"Popen") as spawn:
                    with self.assertRaises(ValueError):
                        run_hosted.capture(["never"],output,"refused",1)
                    spawn.assert_not_called()
                child=mock.Mock();child.pid=4242;child.poll.return_value=0;child.returncode=0
                child.stdout=io.BytesIO(b"");child.stderr=io.BytesIO(b"")
                with mock.patch.object(subprocess,"Popen",return_value=child) as spawn:
                    result,_=run_hosted.capture(["mock-cleanup"],output,"cleanup",1,cleanup=True)
                    self.assertTrue(result["reaped"])
                    self.assertTrue(result["refused"])
                    self.assertTrue(result["cleanup_command"])
                    self.assertTrue(run_hosted.REFUSED.is_set())
                    spawn.assert_called_once()
            finally:
                run_hosted.ARTIFACT_ROOT=old;run_hosted.REFUSED.clear()

    def test_whole_evidence_includes_binary_and_keeps_cleanup_reserve(self):
        import run_hosted
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);output=root/"once";output.mkdir()
            old=run_hosted.ARTIFACT_ROOT;run_hosted.ARTIFACT_ROOT=output
            try:
                with mock.patch.object(run_hosted,"EVIDENCE_LIMIT",128),mock.patch.object(run_hosted,"CLEANUP_RESERVE",32):
                    binary=output/"after.test";binary.write_bytes(b"x"*97)
                    with self.assertRaises(ValueError):run_hosted.evidence_gate()
                    self.assertTrue(run_hosted.REFUSED.is_set())
                    run_hosted.evidence_gate(cleanup=True)
                    binary.write_bytes(b"x"*129)
                    with self.assertRaises(ValueError):run_hosted.evidence_gate(cleanup=True)
            finally:
                run_hosted.ARTIFACT_ROOT=old;run_hosted.REFUSED.clear()

    def test_fallback_refuses_foreign_private_paths_and_container_labels(self):
        import run_supervised
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);(root/"once").mkdir();checkout=root/"runtime"
            context={"tooling_commit":"a"*40,"run_id":"123","fixture_sha256":"b"*64,"checkout":str(checkout),"private_root":str(root/"private")}
            name="cutmy-storage-memory-"+"c"*16
            record={"format":1,"container":name,"volume":name+"-data","owner_label":run_supervised.OWNER_LABEL,
                    "tooling_commit":context["tooling_commit"],"run_id":"123","attempt":1,
                    "fixture_sha256":context["fixture_sha256"],"checkout":str(checkout),
                    "injected_fixture":str(checkout/"internal/app/storage_normal_capacity_pilot_test.go"),
                    "private_root":context["private_root"],"private_env":str(root/"private/postgres.env"),"binary":str(root/"private/after.test")}
            path=root/"once/owned-resources.json";path.write_text(run_supervised.canonical(record).decode())
            self.assertEqual(run_supervised.owned_journal(root,context),record)
            for field,value in (("private_env","/foreign.env"),("binary","/foreign.test"),("run_id","456")):
                wrong=copy.deepcopy(record);wrong[field]=value;path.write_text(run_supervised.canonical(wrong).decode())
                with self.subTest(field=field),self.assertRaises(ValueError):run_supervised.owned_journal(root,context)
            path.write_text(run_supervised.canonical(record).decode())
            def original(argv,out,stem,*args,**kwargs):
                (out/(stem+".stdout.bin")).write_text('{"cutmy.storage-normal-memory.owner":"foreign"}')
                return {"reaped":True,"owned_groups_absent":True,"elapsed_seconds":0,"allocated_wall_seconds":5,
                        "timeout":False,"cancelled":False,"forced":False,"overflow":False,"stream_errors":[],"exit_code":0}
            with mock.patch.object(run_supervised,"check_context"),mock.patch.object(run_supervised,"reconcile_processes",return_value={"pass":True}),mock.patch.object(run_supervised,"supervise",side_effect=original) as execute:
                result=run_supervised.cleanup(root,context,run_supervised.time.monotonic()+30)
                self.assertFalse(result["pass"])
                self.assertIn("exact cleanup owner label",result["errors"])
                self.assertEqual(execute.call_count,2)  # Inspect both owners; never delete either foreign resource.

    def test_reused_pid_is_not_adopted_and_observer_shutdown_has_one_total_allowance(self):
        import run_supervised
        from observe import Observer
        known={42:{"starttime":1,"group":42,"ppid":99}}
        rows={42:{"starttime":2,"group":42,"ppid":88}}
        self.assertEqual(run_supervised.owned_processes(known,rows,set(),77),{})
        first=mock.Mock();second=mock.Mock()
        first.is_alive.return_value=second.is_alive.return_value=False
        observer=Observer({},Path("/unused"),Path("/unused"),None);observer.threads=[first,second]
        with mock.patch("observe.time.monotonic",side_effect=[10,40,65]):observer.stop()
        first.join.assert_called_once_with(50)
        second.join.assert_called_once_with(20)

    def test_vanished_owned_entries_do_not_hide_retained_bytes_or_other_errors(self):
        import run_hosted
        import run_supervised
        def listing(*entries):
            scan=mock.MagicMock();scan.__enter__.return_value=iter(entries);return scan
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)/"state";root.mkdir();output=root/"once";output.mkdir()
            retained=root/"retained";retained.write_bytes(b"data")
            entry=mock.Mock();entry.name="retained";entry.stat.return_value=retained.lstat()
            vanished=mock.Mock();vanished.name="replaced.processes.tmp";vanished.stat.side_effect=FileNotFoundError("vanished owned entry")
            with mock.patch.object(run_hosted,"ARTIFACT_ROOT",output),mock.patch.object(run_hosted,"PRIVATE_ROOT",None),mock.patch.object(os,"scandir",return_value=listing(vanished,entry)):
                run_hosted.evidence_gate()
            with mock.patch.object(os,"scandir",return_value=listing(vanished,entry)):
                self.assertEqual(run_supervised.evidence_bytes(root),4)
            entry.stat.assert_called_with(follow_symlinks=False)
            try:
                with mock.patch.object(run_hosted,"ARTIFACT_ROOT",output),mock.patch.object(run_hosted,"PRIVATE_ROOT",None),mock.patch.object(run_hosted,"EVIDENCE_LIMIT",3),mock.patch.object(run_hosted,"CLEANUP_RESERVE",0),mock.patch.object(os,"scandir",return_value=listing(vanished,entry)):
                    with self.assertRaises(ValueError):run_hosted.evidence_gate()
                self.assertTrue(run_hosted.REFUSED.is_set())
                denied=mock.Mock();denied.stat.side_effect=PermissionError("not vanished")
                for census in (lambda:run_hosted.evidence_gate(),lambda:run_supervised.evidence_bytes(root)):
                    with self.subTest(census=census),mock.patch.object(run_hosted,"ARTIFACT_ROOT",output),mock.patch.object(run_hosted,"PRIVATE_ROOT",None),mock.patch.object(os,"scandir",return_value=listing(denied)):
                        with self.assertRaises(PermissionError):census()
                link=root/"foreign-link";link.symlink_to(retained)
                linked=mock.Mock();linked.stat.return_value=link.lstat()
                for census in (lambda:run_hosted.evidence_gate(),lambda:run_supervised.evidence_bytes(root)):
                    with self.subTest(census=census),mock.patch.object(run_hosted,"ARTIFACT_ROOT",output),mock.patch.object(run_hosted,"PRIVATE_ROOT",None),mock.patch.object(os,"scandir",return_value=listing(linked)):
                        with self.assertRaises(ValueError):census()
            finally:
                run_hosted.REFUSED.clear()

    def test_directory_traversal_denial_is_not_suppressed_by_either_census(self):
        import run_hosted
        import run_supervised
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)/"state";root.mkdir();output=root/"once";output.mkdir()
            for census in (lambda:run_hosted.evidence_gate(),lambda:run_supervised.evidence_bytes(root)):
                with self.subTest(census=census),mock.patch.object(run_hosted,"ARTIFACT_ROOT",output),mock.patch.object(run_hosted,"PRIVATE_ROOT",None),mock.patch.object(os,"scandir",side_effect=PermissionError("mock denied directory")) as scan:
                    with self.assertRaises(PermissionError):census()
                    scan.assert_called_once()
                    self.assertIsInstance(scan.call_args.args[0],int)  # The census is bound to an owned, no-follow directory FD.

    def test_spawn_failure_retains_original_terminal_record_without_a_child(self):
        import run_hosted
        with tempfile.TemporaryDirectory() as temporary:
            output=Path(temporary)/"once";output.mkdir();commands=[]
            try:
                with mock.patch.object(run_hosted,"ARTIFACT_ROOT",output),mock.patch.object(run_hosted,"PRIVATE_ROOT",None),mock.patch.object(run_hosted,"COMMANDS",commands),mock.patch.object(subprocess,"Popen",side_effect=OSError("mock spawn failure")) as spawn:
                    with self.assertRaises(ValueError):run_hosted.capture(["mock-failed"],output,"spawn-failed",1)
                spawn.assert_called_once()
                self.assertEqual(len(commands),1)
                saved=decode((output/"spawn-failed.result.json").read_bytes())
                self.assertEqual(saved,commands[0])
                self.assertIsNone(saved["owned_pid"])
                self.assertIsNone(saved["exit_code"])
                self.assertFalse(saved["spawned"])
                self.assertFalse(saved["reaped"])
                self.assertTrue(saved["refused"])
                self.assertEqual(saved["stream_errors"],["spawn_or_capture_failed:OSError"])
                self.assertEqual((output/"spawn-failed.stdout").read_bytes(),b"")
                self.assertEqual((output/"spawn-failed.stderr").read_bytes(),b"")
            finally:
                run_hosted.REFUSED.clear()

    def test_exit_signal_race_and_stream_failure_keep_the_first_original_result(self):
        import run_hosted
        class BrokenStream:
            def __init__(self):self.first=True
            def read(self,size):
                if self.first:self.first=False;return b"before stream failure"
                raise OSError("mock drain failure")
        for case in ("exit_signal_race","stream_failure"):
            with self.subTest(case=case),tempfile.TemporaryDirectory() as temporary:
                output=Path(temporary)/"once";output.mkdir();commands=[]
                child=mock.Mock();child.pid=4242
                child.stdout=io.BytesIO(b"original output") if case=="exit_signal_race" else BrokenStream()
                child.stderr=io.BytesIO(b"original error")
                child.returncode=None if case=="exit_signal_race" else 0
                child.poll.return_value=child.returncode
                def waited(timeout):child.returncode=0;return 0
                child.wait.side_effect=waited
                clock=mock.patch.object(run_hosted.time,"monotonic",side_effect=[0,2,2,3]) if case=="exit_signal_race" else mock.patch.object(run_hosted.time,"sleep")
                try:
                    with mock.patch.object(run_hosted,"ARTIFACT_ROOT",output),mock.patch.object(run_hosted,"PRIVATE_ROOT",None),mock.patch.object(run_hosted,"COMMANDS",commands),mock.patch.object(subprocess,"Popen",return_value=child),mock.patch.object(run_hosted.os,"killpg",side_effect=ProcessLookupError("already exited")) as terminate,clock:
                        with self.assertRaises(ValueError):run_hosted.capture(["mock-child"],output,case,1)
                    self.assertEqual(len(commands),1)
                    saved=decode((output/(case+".result.json")).read_bytes())
                    self.assertEqual(saved,commands[0])
                    self.assertEqual(saved["owned_pid"],4242)
                    self.assertEqual(saved["exit_code"],0)
                    self.assertTrue(saved["spawned"])
                    self.assertTrue(saved["reaped"])
                    self.assertTrue(saved["refused"])
                    child.wait.assert_called_once_with(timeout=3)
                    self.assertEqual((output/(case+".stderr")).read_bytes(),b"original error")
                    if case=="exit_signal_race":
                        terminate.assert_called_once_with(4242,run_hosted.signal.SIGTERM)
                        self.assertTrue(saved["timeout"])
                        self.assertEqual(saved["stream_errors"],[])
                        self.assertEqual((output/(case+".stdout")).read_bytes(),b"original output")
                    else:
                        terminate.assert_not_called()
                        self.assertFalse(saved["timeout"])
                        self.assertEqual(saved["stream_errors"],["stream_failed"])
                        self.assertEqual((output/(case+".stdout")).read_bytes(),b"before stream failure")
                finally:
                    run_hosted.REFUSED.clear()

    def test_final_ack_stops_sampling_and_uses_final_memory_counters(self):
        from observe import Observer
        required = ["stage_" + s + "_begin" for s in (
            "source_ttl", "foreground_dml", "held_fence", "persisted_audit",
            "foreground_maintenance", "normal_maintenance", "normal_api_bootstrap")]
        for final_oom in (0, 1):
            with self.subTest(final_oom=final_oom), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary); markers = root / "markers"; markers.mkdir()
                marker = {"sequence":1,"phase":"all_fixture_cleanup_end",
                          "utc":"2026-10-11T00:00:00Z","elapsed_ns":1}
                publish(markers / "001.request.json", marker)
                before = sample(2, 1_100_000_000)
                final = sample(3, 1_200_000_000, peak=300)["raw"]
                final["memory.events.local"] = "max 0\noom %d\noom_kill 0\n" % final_oom
                observer = Observer({}, root, markers, None)
                observer.previous = before; observer.samples = 2
                observer.first_memory = parse_memory(sample()["raw"])
                observer.last_memory = parse_memory(before["raw"])
                observer.stack = ["all_fixture_cleanup"]
                observer.phases = required + ["workload_setup_begin", "report_serialization_begin", "all_fixture_cleanup_begin"]
                def boundary(_):
                    self.assertTrue(observer.sample_lock.acquire(blocking=False))
                    observer.sample_lock.release()  # SQL/disk must not block the sampler.
                    return {"mock_boundary":True}
                observer.boundary = boundary
                with mock.patch.object(observer, "check_owned"), mock.patch.object(observer, "raw_memory", side_effect=[before["raw"], final]), mock.patch("observe.time.monotonic_ns", side_effect=[1_150_000_000, 1_200_000_000, 1_200_000_000]):
                    observer.marker_loop()
                self.assertTrue(observer.finished.is_set())
                ack = decode(read(markers / "001.ack.json"))
                endpoint = observer.result()["final_boundary"]
                self.assertEqual(endpoint["sample"], ack["terminal_sample"])
                self.assertEqual(endpoint["sample"], decode(read(root / "final-boundary-sample.json")))
                self.assertEqual(endpoint["sample"]["raw"], final)
                self.assertEqual(endpoint["scope"], [])
                self.assertEqual(observer.previous, before)
                self.assertEqual(observer.result()["last"]["memory.peak"], 200)
                self.assertEqual(endpoint["memory"]["memory.peak"], 300)
                self.assertEqual(observer.result()["oom_counter_increased"], bool(final_oom))
                self.assertEqual(observer.result()["pass"], not final_oom)
                with mock.patch.object(observer, "raw_memory") as extra_read:
                    observer.sample_loop()
                extra_read.assert_not_called()
                self.assertEqual(observer.samples, 2)

    def test_rejected_sample_preserves_candidate_previous_and_first_cause(self):
        from observe import Observer
        for preservation_error in (False, True):
            with self.subTest(preservation_error=preservation_error), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary); previous = sample()
                observer = Observer({}, root, root, None)
                observer.previous = previous; observer.samples = 1
                observer.first_memory = observer.last_memory = parse_memory(previous["raw"])
                candidate_raw = sample(2, 1_201_000_000, peak=250)["raw"]
                preservation = mock.patch("observe.publish", side_effect=OSError("mock preservation failure")) if preservation_error else mock.patch("observe.publish", wraps=publish)
                with mock.patch.object(observer, "raw_memory", return_value=candidate_raw), mock.patch("observe.time.monotonic_ns", return_value=1_201_000_000), preservation:
                    observer.sample_loop()
                candidate = decode((root / "memory-samples.jsonl").read_bytes())
                self.assertEqual(candidate["raw"], candidate_raw)
                self.assertEqual(candidate["monotonic_ns"], 1_201_000_000)
                self.assertEqual(observer.failure, "sample_time_gap")
                self.assertEqual(observer.previous, previous)
                self.assertEqual(observer.samples, 1)
                observer.refuse("later_error")
                self.assertEqual(observer.failure, "sample_time_gap")
                if preservation_error:
                    self.assertEqual(observer.rejection_preservation_error, "OSError")
                else:
                    retained = decode(read(root / "first-rejected-sample.json"))
                    self.assertEqual(retained, {"origin":"periodic","failure":"sample_time_gap",
                                                "candidate":candidate,"previous":previous})
                self.assertFalse(observer.result()["pass"])

    def test_final_endpoint_cannot_hide_late_or_earlier_sample_gap(self):
        from observe import Observer
        for earlier_failure in (False, True):
            with self.subTest(earlier_failure=earlier_failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary); markers = root / "markers"; markers.mkdir()
                marker = {"sequence":1,"phase":"all_fixture_cleanup_end",
                          "utc":"2026-10-11T00:00:00Z","elapsed_ns":1}
                publish(markers / "001.request.json", marker)
                previous = sample(); observer = Observer({}, root, markers, lambda _: {})
                observer.previous = previous; observer.samples = 1
                observer.first_memory = observer.last_memory = parse_memory(previous["raw"])
                observer.stack = ["all_fixture_cleanup"]
                if earlier_failure:
                    observer.refuse("sample_time_gap")
                final_raw = sample(2, 1_200_000_001, peak=250)["raw"]
                with mock.patch.object(observer, "check_owned"), mock.patch.object(observer, "raw_memory", return_value=final_raw) as reads, mock.patch("observe.time.monotonic_ns", side_effect=[1_150_000_000, 1_200_000_001, 1_200_000_001]):
                    observer.marker_loop()
                self.assertEqual(observer.failure, "sample_time_gap")
                self.assertIsNone(observer.final_boundary)
                self.assertFalse((markers / "001.ack.json").exists())
                if earlier_failure:
                    reads.assert_not_called()
                else:
                    retained = decode(read(root / "first-rejected-sample.json"))
                    self.assertEqual(retained["origin"], "final_boundary")
                    self.assertEqual(retained["previous"], previous)
                    self.assertEqual(retained["candidate"]["raw"], final_raw)
                    self.assertEqual(retained["candidate"], decode(read(root / "final-boundary-sample.json")))
                self.assertFalse(observer.result()["pass"])
