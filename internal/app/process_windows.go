//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var processKernel = syscall.NewLazyDLL("kernel32.dll")
var createJobObject = processKernel.NewProc("CreateJobObjectW")
var setJobInformation = processKernel.NewProc("SetInformationJobObject")
var assignJobProcess = processKernel.NewProc("AssignProcessToJobObject")
var terminateJobObject = processKernel.NewProc("TerminateJobObject")
var threadFirst = processKernel.NewProc("Thread32First")
var threadNext = processKernel.NewProc("Thread32Next")
var openThread = processKernel.NewProc("OpenThread")
var resumeThread = processKernel.NewProc("ResumeThread")

type jobBasicLimits struct {
	ProcessTime, JobTime         int64
	Flags                        uint32
	MinWorkingSet, MaxWorkingSet uintptr
	ActiveProcesses              uint32
	Affinity                     uintptr
	Priority, Scheduling         uint32
}

type jobExtendedLimits struct {
	Basic                                                      jobBasicLimits
	IO                                                         [6]uint64
	ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory uintptr
}

type threadEntry struct {
	Size, Usage, ID, Owner      uint32
	BasePriority, DeltaPriority int32
	Flags                       uint32
}

// runProcess starts the primary thread suspended, assigns the process to a
// private job, then resumes it. Thus even an immediately spawned descendant
// inherits the job. Cancellation and closing the job terminate the whole tree.
// A failure to establish containment fails closed before running tool code.
func runProcess(cmd *exec.Cmd) error {
	job, _, err := createJobObject.Call(0, 0)
	if job == 0 {
		return fmt.Errorf("create media process job: %w", err)
	}
	defer syscall.CloseHandle(syscall.Handle(job))
	limits := jobExtendedLimits{}
	limits.Basic.Flags = 0x2000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if ok, _, err := setJobInformation.Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits)); ok == 0 {
		return fmt.Errorf("configure media process job: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x4} // CREATE_SUSPENDED
	var containment sync.Mutex
	cmd.Cancel = func() error {
		containment.Lock()
		defer containment.Unlock()
		ok, _, err := terminateJobObject.Call(job, 1)
		// Cancellation can race Start before job assignment. The suspended
		// direct process cannot have children and must be terminated too.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if ok == 0 {
			return err
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	containment.Lock()
	setupErr := attachAndResume(job, uint32(cmd.Process.Pid))
	if setupErr != nil {
		_ = cmd.Process.Kill()
	}
	containment.Unlock()
	waitErr := cmd.Wait()
	if setupErr != nil {
		return setupErr
	}
	return waitErr
}

func attachAndResume(job uintptr, pid uint32) error {
	// PROCESS_SET_QUOTA | PROCESS_TERMINATE; no unnecessary process rights.
	process, err := syscall.OpenProcess(0x100|0x1, false, pid)
	if err != nil {
		return fmt.Errorf("open suspended media process: %w", err)
	}
	defer syscall.CloseHandle(process)
	if ok, _, err := assignJobProcess.Call(job, uintptr(process)); ok == 0 {
		return fmt.Errorf("contain media process: %w", err)
	}
	// os/exec closes CreateProcess's primary thread handle. Locate its
	// suspended thread through the supported Tool Help API; loader-support
	// threads may also exist before the primary thread first runs.
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("enumerate suspended media thread: %w", err)
	}
	defer syscall.CloseHandle(snapshot)
	entry := threadEntry{Size: uint32(unsafe.Sizeof(threadEntry{}))}
	ok, _, err := threadFirst.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.Owner == pid {
			thread, _, err := openThread.Call(0x2, 0, uintptr(entry.ID)) // THREAD_SUSPEND_RESUME
			if thread == 0 {
				return fmt.Errorf("open suspended media thread: %w", err)
			}
			count, _, resumeErr := resumeThread.Call(thread)
			closeErr := syscall.CloseHandle(syscall.Handle(thread))
			if uint32(count) == 0xffffffff {
				return fmt.Errorf("resume media process: %w", resumeErr)
			}
			if closeErr != nil {
				return closeErr
			}
			if count == 1 {
				return nil
			}
			if count > 1 {
				return errors.New("media process thread remained suspended")
			}
			// Windows can create loader-support threads. A zero count means
			// this thread was already running; find the suspended primary.
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, err = threadNext.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("enumerate media threads: %w", err)
	}
	return fmt.Errorf("suspended media thread missing: %w", os.ErrProcessDone)
}
