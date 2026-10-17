//go:build !windows

package toolrun

import (
	"os/exec"
	"syscall"
)

func runProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		// A successful parent can exit before its descendants, including ones
		// retaining output pipes. Preserve Wait's outcome and end the owned
		// process group even when WaitDelay has closed those pipes.
		defer func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }()
	}
	return cmd.Wait()
}
