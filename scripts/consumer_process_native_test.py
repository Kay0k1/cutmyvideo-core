"""Actual tiny Python children; distinct from mocked signature controls."""
import ctypes
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import release_consumer_process as p


def terminated(pid):
    if os.name == 'nt':
        api = ctypes.WinDLL('kernel32', use_last_error=True)
        api.OpenProcess.argtypes, api.OpenProcess.restype = [ctypes.c_uint32, ctypes.c_int, ctypes.c_uint32], ctypes.c_void_p
        api.GetExitCodeProcess.argtypes, api.GetExitCodeProcess.restype = [ctypes.c_void_p, ctypes.POINTER(ctypes.c_uint32)], ctypes.c_int
        api.CloseHandle.argtypes, api.CloseHandle.restype = [ctypes.c_void_p], ctypes.c_int
        handle = api.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION.
        if not handle:
            if ctypes.get_last_error() == 87:  # ERROR_INVALID_PARAMETER: PID gone.
                return True
            raise OSError('Unable to observe owned child termination.')
        try:
            status = ctypes.c_uint32()
            if not api.GetExitCodeProcess(handle, ctypes.byref(status)):
                raise OSError('Unable to read owned child status.')
            return status.value != 259  # STILL_ACTIVE.
        finally:
            if not api.CloseHandle(handle):
                raise OSError('Unable to close owned process observation handle.')
    # Observe only the owned PID. Zombies have terminated, even if the CI host's
    # reaper has not yet removed their process table entries.
    result = subprocess.run(['ps', '-o', 'stat=', '-p', str(pid)], stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, timeout=2)
    if result.returncode == 1 and not result.stdout:
        return True
    if result.returncode != 0 or len(result.stdout) > 128:
        raise OSError('Unable to observe owned child termination.')
    return result.stdout.decode('ascii').strip().startswith('Z')


class NativeConsumerProcesses(unittest.TestCase):
    def test_hostile_caller_git_config_and_environment_do_not_redirect_commands(self):
        with tempfile.TemporaryDirectory() as temporary:
            repo = Path(temporary) / 'caller'
            subprocess.run(['git', 'init', '-q', str(repo)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True, timeout=2)
            config = repo / '.git' / 'config'
            with config.open('a') as file:
                file.write('\n[url "https://example.invalid/hostile/"]\n\tinsteadOf = https://github.com/\n')
            probe = ['git', 'config', '--local', '--get-regexp', '^url[.]']
            observed = subprocess.run(probe, cwd=repo, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=2)
            self.assertEqual(observed.returncode, 0, 'Fixture must contain a real local URL rewrite.')
            previous = Path.cwd()
            try:
                os.chdir(repo)
                with patch.dict(os.environ, GIT_CONFIG=str(config), GIT_COMMON_DIR=str(repo / '.git'),
                                GIT_DIR=str(repo / '.git'), GIT_WORK_TREE=str(repo)):
                    with self.assertRaises(ValueError):
                        p.output(probe, 256, time.monotonic() + 2)
            finally:
                os.chdir(previous)

    def test_normal_output_and_owned_descendant_closed_after_parent_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            marker = Path(temporary) / 'owned-pid'
            code = ('import pathlib,subprocess,sys; '
                    'child=subprocess.Popen([sys.executable,"-I","-S","-c","import time; time.sleep(30)"]); '
                    'pathlib.Path(sys.argv[1]).write_text(str(child.pid)); sys.stdout.buffer.write(b"verified fixture\\n")')
            self.assertEqual(p.output([sys.executable, '-I', '-S', '-c', code, str(marker)], 128, time.monotonic() + 4), b'verified fixture\n')
            self.assert_terminated(int(marker.read_text()))

    def test_flood_is_refused_without_loading_unbounded_output(self):
        code = 'import os,time\nfor _ in range(256): os.write(1,b"x"*4096)\ntime.sleep(30)'
        started = time.monotonic()
        with self.assertRaises(ValueError):
            p.output([sys.executable, '-I', '-S', '-c', code], 256 * 1024, time.monotonic() + 4)
        self.assertLess(time.monotonic() - started, 5)

    def test_timeout_closes_owned_descendant_and_leaves_no_inherited_pipe(self):
        with tempfile.TemporaryDirectory() as temporary:
            marker = Path(temporary) / 'owned-pid'
            code = ('import pathlib,subprocess,sys,time; '
                    'child=subprocess.Popen([sys.executable,"-I","-S","-c","import time; time.sleep(30)"]); '
                    'pathlib.Path(sys.argv[1]).write_text(str(child.pid)); time.sleep(30)')
            started = time.monotonic()
            with self.assertRaises(ValueError):
                p.output([sys.executable, '-I', '-S', '-c', code, str(marker)], 128, time.monotonic() + 1)
            self.assertLess(time.monotonic() - started, 5)
            self.assertTrue(marker.is_file(), 'Fixture must observe a real descendant before timeout.')
            self.assert_terminated(int(marker.read_text()))

    def assert_terminated(self, pid):
        stop = time.monotonic() + 2
        while time.monotonic() < stop:
            if terminated(pid):
                return
            time.sleep(0.02)
        self.fail('Owned child remained running after consumer command cleanup.')


if __name__ == '__main__':
    unittest.main()
