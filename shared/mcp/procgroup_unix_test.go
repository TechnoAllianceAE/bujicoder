//go:build !windows

package mcp

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestShutdownAllReapsGrandchildren covers the orphan leak: MCP servers are
// commonly launched through a wrapper (`npx`, `uv`, a shell script) that spawns
// the real server as a child. The SDK only signals the direct child, so without
// a process-group kill the real server survives BujiCoder's exit.
func TestShutdownAllReapsGrandchildren(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	cfg := helperConfig(t, "wrapper", "spawner")
	t.Setenv(helperChildPIDVa, pidFile)

	m := NewManager([]ServerConfig{cfg})
	if _, err := m.ensureStarted(context.Background(), "wrapper", cfg); err != nil {
		t.Fatalf("ensureStarted: %v", err)
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
		t.Fatalf("grandchild %d is not running before shutdown: %v", pid, err)
	}

	m.ShutdownAll()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return // grandchild is gone
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived ShutdownAll: orphaned MCP server process", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
