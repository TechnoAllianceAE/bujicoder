//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in its own process group so that the whole
// process tree can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup kills the child and everything it spawned. Killing only the
// child leaves orphaned grandchildren holding the output pipe open, which keeps
// cmd.Wait blocked forever.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// The process group id equals the child's pid because of Setpgid above.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
