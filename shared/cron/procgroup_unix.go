//go:build !windows

package cron

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group so that the whole
// group (the command and anything it spawned) can be signalled without touching BujiCoder's own process group.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs any surviving members of the child's process group.
// A `bash -c` command routinely spawns children of its own; killing only the
// shell leaves them running past the timeout. Because setProcessGroup
// made the child a group leader, the group id equals the child pid; POSIX keeps
// that id reserved while any member of the group is alive, so this signal
// either reaches leftover group members or fails with ESRCH — it can never hit
// an unrelated process, nor BujiCoder itself.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
