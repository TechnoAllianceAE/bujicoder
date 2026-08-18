//go:build windows

package lsp

import "os/exec"

// setProcessGroup is a no-op on Windows: process groups are not created via
// SysProcAttr.Setpgid there.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills the server directly on Windows. Terminating a whole
// process tree requires a job object, which is out of scope here.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
