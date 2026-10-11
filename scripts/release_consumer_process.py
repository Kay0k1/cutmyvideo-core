"""Bound consumer CLI output/time and own child processes on Unix and Windows."""
import ctypes
import os
import signal
import subprocess
import sys
import tempfile
import time


class CommandRefused(ValueError):
    pass


def require(value):
    if not value:
        raise CommandRefused('Verification command or process containment failed.')


class BasicLimits(ctypes.Structure):
    _fields_ = [('process_time', ctypes.c_int64), ('job_time', ctypes.c_int64), ('flags', ctypes.c_uint32),
                ('minimum', ctypes.c_size_t), ('maximum', ctypes.c_size_t), ('active', ctypes.c_uint32),
                ('affinity', ctypes.c_size_t), ('priority', ctypes.c_uint32), ('scheduling', ctypes.c_uint32)]


class IOCounters(ctypes.Structure):
    _fields_ = [(name, ctypes.c_uint64) for name in ('read_ops', 'write_ops', 'other_ops', 'read_bytes', 'write_bytes', 'other_bytes')]


class ExtendedLimits(ctypes.Structure):
    _fields_ = [('basic', BasicLimits), ('io', IOCounters), ('process_memory', ctypes.c_size_t),
                ('job_memory', ctypes.c_size_t), ('peak_process', ctypes.c_size_t), ('peak_job', ctypes.c_size_t)]


class ThreadEntry(ctypes.Structure):
    _fields_ = [('size', ctypes.c_uint32), ('usage', ctypes.c_uint32), ('thread_id', ctypes.c_uint32),
                ('owner', ctypes.c_uint32), ('priority', ctypes.c_int32), ('delta', ctypes.c_int32), ('flags', ctypes.c_uint32)]


class WindowsJob:
    """Attach a suspended child before any child code can spawn descendants."""
    def __init__(self, kernel=None):
        require(kernel is not None or sys.implementation.name == 'cpython')
        self.kernel = kernel or ctypes.WinDLL('kernel32', use_last_error=True)
        api = self.kernel
        for name, result, arguments in (
                ('CreateJobObjectW', ctypes.c_void_p, [ctypes.c_void_p, ctypes.c_wchar_p]),
                ('SetInformationJobObject', ctypes.c_int, [ctypes.c_void_p, ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]),
                ('AssignProcessToJobObject', ctypes.c_int, [ctypes.c_void_p, ctypes.c_void_p]),
                ('CreateToolhelp32Snapshot', ctypes.c_void_p, [ctypes.c_uint32, ctypes.c_uint32]),
                ('Thread32First', ctypes.c_int, [ctypes.c_void_p, ctypes.POINTER(ThreadEntry)]),
                ('Thread32Next', ctypes.c_int, [ctypes.c_void_p, ctypes.POINTER(ThreadEntry)]),
                ('OpenThread', ctypes.c_void_p, [ctypes.c_uint32, ctypes.c_int, ctypes.c_uint32]),
                ('ResumeThread', ctypes.c_uint32, [ctypes.c_void_p]),
                ('CloseHandle', ctypes.c_int, [ctypes.c_void_p])):
            function = getattr(api, name)
            function.restype, function.argtypes = result, arguments
        self.handle = api.CreateJobObjectW(None, None)
        require(self.handle)
        try:
            limits = ExtendedLimits()
            limits.basic.flags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
            require(api.SetInformationJobObject(self.handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)))
        except BaseException:
            self.close()
            raise

    def initial_thread(self, pid):
        api = self.kernel
        snapshot = api.CreateToolhelp32Snapshot(4, 0)  # TH32CS_SNAPTHREAD.
        require(snapshot and snapshot != ctypes.c_void_p(-1).value)
        try:
            entry = ThreadEntry()
            entry.size = ctypes.sizeof(entry)
            found = []
            require(api.Thread32First(snapshot, ctypes.byref(entry)))
            count = 0
            while True:
                count += 1
                require(count <= 100000)
                if entry.owner == pid:
                    found.append(entry.thread_id)
                if not api.Thread32Next(snapshot, ctypes.byref(entry)):
                    require(ctypes.get_last_error() == 18)  # ERROR_NO_MORE_FILES.
                    break
            require(len(found) == 1)
            return found[0]
        finally:
            require(api.CloseHandle(snapshot))

    def attach_and_resume(self, process):
        api = self.kernel
        require(api.AssignProcessToJobObject(self.handle, int(process._handle)))
        thread = api.OpenThread(2, False, self.initial_thread(process.pid))  # THREAD_SUSPEND_RESUME.
        require(thread)
        try:
            require(api.ResumeThread(thread) == 1)
        finally:
            require(api.CloseHandle(thread))

    def close(self):
        if self.handle:
            handle, self.handle = self.handle, None
            require(self.kernel.CloseHandle(handle))


def output(argv, limit, deadline=None):
    timeout = min(60, deadline - time.monotonic()) if deadline is not None else 60
    require(timeout > 0 and type(limit) is int and limit > 0)
    stop = time.monotonic() + timeout
    env = dict(os.environ, GH_HOST='github.com', GH_PROMPT_DISABLED='1', GIT_TERMINAL_PROMPT='0',
               GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull, GIT_CONFIG_COUNT='0')
    for key in ('GIT_CONFIG_PARAMETERS', 'GIT_CONFIG', 'GIT_DIR', 'GIT_WORK_TREE', 'GIT_COMMON_DIR'):
        env.pop(key, None)
    process, job = None, None
    # Even git ls-remote consults a caller checkout's local URL rewrites. An
    # empty private cwd and discovery ceiling exclude all caller repositories.
    with tempfile.TemporaryDirectory(prefix='cutmy-consumer-command-') as temporary, tempfile.TemporaryFile() as stream:
        env['GIT_CEILING_DIRECTORIES'] = os.pathsep.join((temporary, os.path.dirname(temporary)))
        try:
            options = {'creationflags': 0x4} if os.name == 'nt' else {'start_new_session': True}
            if os.name == 'nt':
                job = WindowsJob()
            process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=stream, stderr=subprocess.DEVNULL,
                                       env=env, cwd=temporary, **options)
            if job:
                job.attach_and_resume(process)
            while True:
                require(os.fstat(stream.fileno()).st_size <= limit and time.monotonic() < stop)
                status = process.poll()
                if status is not None:
                    require(status == 0)
                    break
                time.sleep(0.02)
            # Close the job/process group before reading: descendants cannot
            # retain streams or modify verification output after parent exit.
        finally:
            try:
                if job:
                    job.close()
                elif process:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
            finally:
                if process and process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
        require(os.fstat(stream.fileno()).st_size <= limit)
        stream.seek(0)
        raw = stream.read(limit + 1)
        require(len(raw) <= limit)
        return raw
