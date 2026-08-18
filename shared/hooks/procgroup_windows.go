//go:build windows

package hooks

import "os/exec"

// setProcessGroup is a no-op on Windows: process groups are not created via
// SysProcAttr.Setpgid there.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills the child directly on Windows. Killing a whole process
// tree requires a job object, which the MCP SDK does not set up; the SDK's own
// Close already terminates the direct child, so this is a best-effort sweep.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
