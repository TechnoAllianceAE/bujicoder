//go:build !windows

package lsp

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCloseReapsGrandchildren covers the orphan leak: language servers launched
// through a wrapper (npx, uv, a shell script) spawn the real server as a child,
// so signalling only the direct child leaves it running after BujiCoder exits.
func TestCloseReapsGrandchildren(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	cfg := helperServerConfig(t, "spawner")
	t.Setenv(helperPIDFileEnv, pidFile)

	c, err := Start(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("helper did not record its grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("bad pid %q: %v", raw, err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("grandchild %d is not running before Close: %v", pid, err)
	}

	c.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return // grandchild is gone
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived Close: orphaned language server process", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
