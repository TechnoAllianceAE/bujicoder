//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP; it detaches the child from
// the parent's console control events.
const createNewProcessGroup = 0x00000200

// setProcessGroup starts the child in its own process group.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

// killProcessGroup terminates the child. Windows has no direct equivalent of
// killing a process group by id without a job object, so only the child is
// terminated here.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
