//go:build !windows

package lsp

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the language server in its own process group so the whole
// group can be signalled without touching BujiCoder's own process group.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs any surviving members of the server's process group.
// Language servers launched through a wrapper (npx, a shell script) spawn the
// real server as a child; killing only the direct child orphans it. Since
// setProcessGroup made the child a group leader, the group id equals its pid,
// which POSIX keeps reserved while any group member is alive — so this signal
// either reaches leftovers or fails with ESRCH.
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
