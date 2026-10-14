import errno
import os
import pathlib
import signal
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock, patch

from cutmyvideo import LocalCutmy, LocalError

HELPER = pathlib.Path(__file__).with_name("cli_helper.py")


class LocalTests(unittest.TestCase):
    def client(self, mode="success", **kwargs):
        return LocalCutmy([sys.executable, str(HELPER), mode], **kwargs)

    def test_argv_unicode_private_plan_and_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "a ; $(unsafe) ü.mp4"
            plan = {"version": 1, "input": "source ; $(unsafe) ü.mp4", "ranges": [{"start_ms": 0, "end_ms": 10}], "options": {}}
            original = dict(plan)
            for method in (self.client().preflight, self.client().execute_plan):
                result = method(plan, output, timeout=2)
                self.assertEqual(result["args"][0], "--json-errors")
                self.assertIn(str(output.absolute()), result["args"])
                self.assertFalse(pathlib.Path(result["plan_path"]).exists())
                self.assertEqual(result["plan"]["ranges"], plan["ranges"])
                self.assertEqual(plan, original)
            self.assertEqual(self.client().inspect(output)["duration_ms"], 1000)
            self.assertFalse(output.exists())

    def test_partial_outputs_and_unknown_error_codes(self):
        with self.assertRaises(LocalError) as failed:
            self.client("partial").inspect("input")
        self.assertEqual(failed.exception.code, "output_exists")
        self.assertEqual(failed.exception.outputs[0]["path"], "first.mp4")
        self.assertEqual(failed.exception.exit_code, 1)
        with self.assertRaises(LocalError) as future:
            self.client("unknown-error").inspect("input")
        self.assertEqual(future.exception.code, "future_media_code")

    def test_invalid_success_and_capture_limit(self):
        for mode in ("malformed", "wrong-shape"):
            with self.assertRaises(LocalError) as invalid:
                self.client(mode).inspect("input")
            self.assertEqual(invalid.exception.code, "invalid_response")
        started = time.monotonic()
        with self.assertRaises(LocalError) as oversized:
            self.client("large").inspect("input", timeout=5)
        self.assertEqual(oversized.exception.code, "response_too_large")
        self.assertLess(time.monotonic() - started, 4)

    def test_deadline_and_posix_descendant_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            pidfile = pathlib.Path(directory) / "pid"
            started = time.monotonic()
            with self.assertRaises(LocalError) as failed:
                self.client("sleep", env={"SDK_TEST_PID": str(pidfile)}).inspect("input", timeout=0.2)
            self.assertEqual(failed.exception.code, "timeout")
            self.assertLess(time.monotonic() - started, 4)
            if os.name == "posix" and pathlib.Path("/proc").is_dir():
                child = int(pidfile.read_text(encoding="ascii"))
                status = pathlib.Path(f"/proc/{child}/stat")
                self.assertTrue(not status.exists() or status.read_text().split()[2] == "Z")

    def test_argument_validation_and_missing_tool(self):
        for invalid in (0, -1, float("nan"), float("inf")):
            with self.assertRaises(ValueError):
                self.client().inspect("input", timeout=invalid)
        with self.assertRaises(ValueError):
            self.client().preflight({"input": "input", "value": "x" * (1 << 20)}, ".")
        with self.assertRaises(ValueError):
            self.client().preflight({"input": "input", "value": float("nan")}, ".")
        with self.assertRaises(LocalError) as unavailable:
            LocalCutmy("cutmy-does-not-exist-sdk-fixture").inspect("input")
        self.assertEqual(unavailable.exception.code, "tool_unavailable")

    @unittest.skipUnless(os.name == "posix", "POSIX process-group shutdown")
    def test_darwin_exited_group_permission_error_keeps_timeout(self):
        original = os.killpg
        signals = []

        def signal_group(pid, signum):
            signals.append(signum)
            if signum == signal.SIGKILL:
                raise PermissionError(errno.EPERM, "No live group members")
            return original(pid, signum)

        with tempfile.TemporaryDirectory() as directory:
            pidfile = pathlib.Path(directory) / "pid"
            with patch("sys.platform", "darwin"), patch("os.killpg", side_effect=signal_group):
                with self.assertRaises(LocalError) as failed:
                    self.client("sleep", env={"SDK_TEST_PID": str(pidfile)}).inspect("input", timeout=0.2)
            self.assertEqual(failed.exception.code, "timeout")
            self.assertEqual(signals, [signal.SIGTERM, signal.SIGKILL])

        leader = Mock()
        leader.poll.return_value = None
        with patch("sys.platform", "darwin"), patch("os.killpg", side_effect=PermissionError(errno.EPERM, "Denied")):
            with self.assertRaises(PermissionError):
                LocalCutmy._signal_group(leader, signal.SIGKILL)
        leader.poll.return_value = 0
        with patch("sys.platform", "linux"), patch("os.killpg", side_effect=PermissionError(errno.EPERM, "Denied")):
            with self.assertRaises(PermissionError):
                LocalCutmy._signal_group(leader, signal.SIGKILL)


if __name__ == "__main__":
    unittest.main()
