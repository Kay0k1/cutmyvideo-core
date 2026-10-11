"""Focused failure controls; exactly one test creates inert owned Python children."""
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
ALLOW_INERT_CHILDREN = False
CHILD_LAUNCHES = []
DENIED_OPERATIONS = []


def audit(event, args):
    if event.startswith("socket.") or event in {"os.system", "os.fork", "os.forkpty", "os.posix_spawn", "os.exec", "os.spawn"}:
        DENIED_OPERATIONS.append(event)
        raise AssertionError("focused controls deny network and arbitrary execution")
    if event.startswith("subprocess."):
        argv = args[1] if event == "subprocess.Popen" else []
        if not ALLOW_INERT_CHILDREN or argv[:4] != [sys.executable, "-I", "-B", "-c"]:
            DENIED_OPERATIONS.append(event)
            raise AssertionError("only declared inert Python children are permitted")
        CHILD_LAUNCHES.append(list(argv[:4]))


sys.addaudithook(audit)
import run_hosted as hosted
import run_once as once
from protocol import Refused


class FinishedChild:
    pid = 412345
    returncode = 0

    def __init__(self, callback=None):
        self.stdout = io.BytesIO(b"first original stdout\n")
        self.stderr = io.BytesIO(b"first original stderr\n")
        self.callback = callback

    def poll(self):
        if self.callback:
            self.callback()
        return self.returncode

    def wait(self, timeout=None):
        return self.returncode


def good_result():
    return {"exit_code": 0, "reaped": True, "owned_groups_absent": True,
            "elapsed_seconds": 1, "allocated_wall_seconds": 5, "timeout": False,
            "cancelled": False, "forced": False, "overflow": False, "stream_errors": []}


class FocusedControls(unittest.TestCase):
    def test_refusal_before_capture_never_spawns(self):
        with tempfile.TemporaryDirectory() as directory:
            run = once.OnceRun(Path(directory)); run.refusal.set()
            with mock.patch.object(once.subprocess, "Popen") as spawn:
                with self.assertRaises(Refused):
                    run.capture(["forbidden-mutating-command"], "refused")
            spawn.assert_not_called()
            self.assertTrue(run.refusal.is_set())
            self.assertEqual(run.commands, [])

    def test_interruption_after_exit_zero_stays_failure_with_original_streams(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); run = once.OnceRun(root)
            child = FinishedChild(run.refusal.set)
            with mock.patch.object(once.subprocess, "Popen", return_value=child):
                with self.assertRaises(Refused):
                    run.checked(["inert-mock"], "first")
            result = run.commands[0]
            self.assertEqual(result["exit_code"], 0)
            self.assertTrue(result["reaped"] and result["refused"])
            self.assertEqual((root / "first.stdout").read_bytes(), b"first original stdout\n")
            self.assertEqual((root / "first.stderr").read_bytes(), b"first original stderr\n")

    def test_cleanup_allowed_without_clearing_sticky_interruption(self):
        with tempfile.TemporaryDirectory() as directory:
            run = once.OnceRun(Path(directory)); run.refusal.set(); run.interrupted.set()
            with mock.patch.object(once.subprocess, "Popen", return_value=FinishedChild()):
                self.assertEqual(run.checked(["inert-mock-cleanup"], "cleanup", cleanup=True), b"first original stdout\n")
            self.assertTrue(run.refusal.is_set() and run.interrupted.is_set())
            self.assertTrue(run.commands[0]["cleanup_command"])

    def test_first_timeout_keeps_exit_and_both_streams(self):
        class TimeoutChild(FinishedChild):
            returncode = None
            def wait(self, timeout=None):
                self.returncode = -signal.SIGTERM
                return self.returncode
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); run = once.OnceRun(root)
            child = TimeoutChild()
            with mock.patch.object(once.subprocess, "Popen", return_value=child), mock.patch.object(once.os, "killpg") as kill, mock.patch.object(once.time, "monotonic", side_effect=[0, 3, 3, 3]):
                result, stdout = run.capture(["inert-timeout-mock"], "timeout", timeout=1)
            kill.assert_called_once_with(child.pid, signal.SIGTERM)
            self.assertEqual(result["exit_code"], -signal.SIGTERM)
            self.assertTrue(result["timeout"] and result["reaped"])
            self.assertEqual(stdout, b"first original stdout\n")
            self.assertEqual((root / "timeout.stderr").read_bytes(), b"first original stderr\n")

    def test_incomplete_summary_or_late_exit_never_passes(self):
        original = good_result()
        summary = {"complete": False, "pass": True, "cleanup": True}
        self.assertFalse(hosted.once_pass(original, summary))
        summary["complete"] = True
        self.assertTrue(hosted.once_pass(original, summary))
        summary["refused"] = True
        self.assertFalse(hosted.once_pass(original, summary))
        original["elapsed_seconds"] = 6
        self.assertFalse(hosted.successful(original))

    def test_spawn_failure_preserves_capture_and_restores_subreaper(self):
        libc = mock.Mock(); libc.prctl.return_value = 0
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with mock.patch.object(hosted, "snapshot_processes", return_value={}), mock.patch.object(hosted, "enable_subreaper", return_value=(libc, 0)), mock.patch.object(hosted.subprocess, "Popen", side_effect=OSError("synthetic spawn refusal")):
                result = hosted.supervise(["inert-failing-mock"], root, "first", root, {}, threading.Event(), wall=2, reserve=.5)
            self.assertIsNone(result["owned_pid"])
            self.assertFalse(hosted.successful(result))
            self.assertIn("synthetic spawn refusal", result["stream_errors"][0])
            self.assertEqual((root / "first.stdout.bin").read_bytes(), b"")
            self.assertEqual((root / "first.stderr.bin").read_bytes(), b"")
            self.assertEqual(json.loads((root / "first.result.json").read_bytes())["exit_code"], None)
            libc.prctl.assert_called_once_with(36, 0, 0, 0, 0)

    def test_pid_reuse_cannot_admit_foreign_lineage(self):
        def row(ppid, starttime, group):
            return {"ppid": ppid, "starttime": starttime, "group": group, "state": "S"}
        known = {10: row(1000, 3, 10), 42: row(999, 5, 42)}
        rows = {10: row(1000, 3, 10), 11: row(10, 4, 11), 12: row(1000, 7, 12),
                42: row(999, 9, 42), 43: row(42, 10, 43), 20: row(1000, 8, 20),
                21: row(20, 11, 21)}
        current = hosted.owned_processes(known, rows, {(20, 8)}, 1000)
        self.assertEqual(set(current), {10, 11, 12})
        self.assertNotIn(43, known)
        self.assertNotIn(20, known)
        self.assertNotIn(21, known)
        self.assertEqual(known[42]["starttime"], 5)

    def test_reused_process_journal_identity_is_never_signalled(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            hosted.save(root, "outer-once.processes.json", {"owned_pid": 42, "processes": {
                "42": {"starttime": 5, "group": 42}}})
            rows = {42: {"starttime": 9, "group": 42, "ppid": 999, "state": "S"}}
            with mock.patch.object(hosted, "snapshot_processes", return_value=rows), mock.patch.object(hosted.os, "killpg") as kill:
                result = hosted.reconcile_processes(root, time.monotonic() + 1)
            self.assertTrue(result["pass"])
            kill.assert_not_called()

    def context(self, root, private_preexisting=False):
        temp = root / "runner"; temp.mkdir()
        tooling = root / "tooling"; here = tooling / "benchmarks/populated004to005"; here.mkdir(parents=True)
        checkout = tooling / ".populated-runtime"; (checkout / "internal/app").mkdir(parents=True)
        context = {"runner_temp": str(temp), "checkout": str(checkout),
                   "private_root": str(temp / "populated100k-private"), "tooling_commit": "a" * 40,
                   "run_id": "inert-control", "attempt": 1, "freeze_sha256": hosted.FREEZE_SHA,
                   "wrapper_sha256": hosted.sha(Path(hosted.__file__).read_bytes()),
                   "private_preexisting": private_preexisting, "fixture_preexisting": False}
        env = {"RUNNER_TEMP": str(temp), "GITHUB_SHA": "a" * 40, "GITHUB_RUN_ID": "inert-control"}
        return here, context, env

    def resource_journal(self, state, context):
        (state / "once").mkdir()
        name = "cutmy-populated100k-" + "b" * 16
        hosted.save(state / "once", "owned-resources.json", {
            "format": 1, "container": name, "volume": name + "-data", "fixture_sha256": hosted.FIXTURE_SHA,
            "private_root": context["private_root"], "private_env": str(Path(context["private_root"]) / "postgres.env"),
            "binary": str(Path(context["private_root"]) / "populated100k.test"), "checkout": context["checkout"],
            "injected_fixture": str(Path(context["checkout"]) / "internal/app/populated004_to005_100k_test.go")})
        return name

    def test_preexisting_unrelated_private_path_survives_refusal(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); here, context, env = self.context(root, private_preexisting=True)
            private = Path(context["private_root"]); private.mkdir(); marker = private / "foreign.txt"; marker.write_bytes(b"keep foreign bytes")
            state = root / "evidence"; state.mkdir()
            with mock.patch.object(hosted, "HERE", here), mock.patch.dict(os.environ, env), mock.patch.object(hosted, "supervise") as operation:
                result = hosted.cleanup(state, context, time.monotonic() + 1)
            self.assertFalse(result["pass"])
            self.assertEqual(marker.read_bytes(), b"keep foreign bytes")
            operation.assert_not_called()

    def test_attempt_without_pid_journal_preserves_owned_private_and_fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); here, context, env = self.context(root)
            private = Path(context["private_root"]); private.mkdir(); marker = private / "owned.txt"; marker.write_bytes(b"preserve until child absence proven")
            fixture = Path(context["checkout"]) / "internal/app/populated004_to005_100k_test.go"
            fixture.write_bytes(Path(once.__file__).with_name("fixture_test.go.txt").read_bytes())
            state = root / "evidence"; state.mkdir(); self.resource_journal(state, context)
            hosted.save(state, "outer-once.invocation.json", {"attempt": 1})
            def operation(argv, output, operation_name, *args, **kwargs):
                result = good_result(); result["exit_code"] = 1 if "inspect" in argv else 0
                (output / (operation_name + ".stdout.bin")).write_bytes(b"")
                return result
            with mock.patch.object(hosted, "HERE", here), mock.patch.dict(os.environ, env), mock.patch.object(hosted, "supervise", side_effect=operation):
                result = hosted.cleanup(state, context, time.monotonic() + 2)
            self.assertFalse(result["pass"])
            self.assertTrue(result["process_reconciliation"]["incomplete_ownership"])
            self.assertEqual(marker.read_bytes(), b"preserve until child absence proven")
            self.assertEqual(hosted.sha(fixture.read_bytes()), hosted.FIXTURE_SHA)

    def test_missing_path_admission_context_refuses_without_delete(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); here, context, env = self.context(root)
            private = Path(context["private_root"]); private.mkdir()
            context.pop("private_preexisting")
            state = root / "evidence"; state.mkdir()
            with mock.patch.object(hosted, "HERE", here), mock.patch.dict(os.environ, env), mock.patch.object(hosted, "supervise") as operation:
                with self.assertRaises(ValueError): hosted.cleanup(state, context, time.monotonic() + 1)
            self.assertTrue(private.is_dir())
            operation.assert_not_called()

    def test_foreign_resource_label_is_preserved_and_owned_absence_checked(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); here, context, env = self.context(root)
            state = root / "evidence"; state.mkdir(); name = self.resource_journal(state, context)
            commands = []
            def operation(argv, output, operation_name, *args, **kwargs):
                commands.append(argv)
                result = good_result()
                if argv[1:2] == ["inspect"]:
                    raw = b'{"cutmy.populated100k.owner":"foreign-owner"}\n'
                elif argv[1:3] == ["volume", "inspect"]:
                    result["exit_code"] = 1; raw = b""
                else:
                    raw = b""
                (output / (operation_name + ".stdout.bin")).write_bytes(raw)
                return result
            with mock.patch.object(hosted, "HERE", here), mock.patch.dict(os.environ, env), mock.patch.object(hosted, "supervise", side_effect=operation):
                result = hosted.cleanup(state, context, time.monotonic() + 2)
            self.assertFalse(result["pass"])
            self.assertTrue(any("exact cleanup owner label" in error for error in result["errors"]))
            self.assertFalse(any(argv[1] == "rm" for argv in commands))
            self.assertTrue(any(argv[1:3] == ["volume", "ls"] and "name=^" + name + "-data$" in argv for argv in commands))

    def test_owned_journal_cannot_rebind_foreign_path(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); _, context, _ = self.context(root)
            state = root / "evidence"; state.mkdir(); self.resource_journal(state, context)
            path = state / "once/owned-resources.json"; value = json.loads(path.read_bytes())
            value["private_root"] = str(root / "foreign")
            path.write_bytes(hosted.canonical(value))
            with self.assertRaises(ValueError): hosted.owned_journal(state, context)

    def test_supervisor_kills_detached_descendant_and_preserves_unrelated_child(self):
        global ALLOW_INERT_CHILDREN
        ALLOW_INERT_CHILDREN = True
        control = None
        try:
            control = subprocess.Popen([sys.executable, "-I", "-B", "-c", "import time; time.sleep(30)"], stdin=subprocess.DEVNULL,
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
            descendant = "import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(30)"
            script = ("import signal,subprocess,sys,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); "
                      "child=subprocess.Popen([sys.executable,'-I','-B','-c'," + repr(descendant) + "],start_new_session=True); "
                      "print('desc_pid='+str(child.pid),flush=True); print('first original synthetic stdout',flush=True); "
                      "print('first original synthetic stderr',file=sys.stderr,flush=True); time.sleep(30)")
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = hosted.supervise([sys.executable, "-I", "-B", "-c", script], root, "synthetic-owned", root,
                                          os.environ.copy(), threading.Event(), wall=3, reserve=1, grace=.25)
                stdout = (root / "synthetic-owned.stdout.bin").read_bytes()
                stderr = (root / "synthetic-owned.stderr.bin").read_bytes()
                descendant_pid = int(stdout.split(b"desc_pid=", 1)[1].splitlines()[0])
                self.assertEqual(result["exit_code"], -signal.SIGKILL)
                self.assertTrue(result["timeout"] and result["forced"] and result["reaped"] and result["owned_groups_absent"])
                self.assertLessEqual(result["elapsed_seconds"], result["allocated_wall_seconds"])
                self.assertIn(b"first original synthetic stdout\n", stdout)
                self.assertEqual(stderr, b"first original synthetic stderr\n")
                self.assertIn(descendant_pid, result["observed_processes"])
                self.assertTrue(any(row["pid"] == descendant_pid for row in result["adopted_reaped"]))
                self.assertNotIn(control.pid, result["observed_processes"])
                self.assertIsNone(control.poll())
                self.assertFalse(hosted.successful(result))
        finally:
            if control is not None:
                if control.poll() is None: os.killpg(control.pid, signal.SIGKILL)
                control.wait(timeout=3)
            ALLOW_INERT_CHILDREN = False


if __name__ == "__main__":
    result = unittest.main(verbosity=2, exit=False).result
    print("focused audit: inert launches=" + str(len(CHILD_LAUNCHES)) + "; denied actual operations=" + str(len(DENIED_OPERATIONS)))
    raise SystemExit(0 if result.wasSuccessful() and not DENIED_OPERATIONS and len(CHILD_LAUNCHES) == 2 else 1)
