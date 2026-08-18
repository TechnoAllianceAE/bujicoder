package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

const (
	// terminalCommandTimeout bounds a single run_terminal_command execution.
	// Without it a command that never returns (an interactive prompt, a server
	// started in the foreground) blocks the agent forever.
	terminalCommandTimeout = 2 * time.Minute

	// searchCommandTimeout bounds the helper processes used by code_search.
	searchCommandTimeout = 30 * time.Second

	// maxCommandOutput caps how much command output is retained. A chatty
	// command (`cat` on a huge file, a build log) would otherwise be buffered
	// in full and can exhaust memory.
	maxCommandOutput = 1 << 20 // 1 MiB
)

// boundedBuffer is an io.Writer that retains at most limit bytes and counts
// what it drops.
type boundedBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	limit   int
	dropped int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - b.buf.Len()
	if room < 0 {
		room = 0
	}
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		b.buf.Write(p[:room])
	}
	b.dropped += len(p) - room
	return len(p), nil
}

// String returns the retained output, with a truncation notice when output was
// dropped.
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if b.dropped > 0 {
		s += fmt.Sprintf("\n[output truncated: %d further bytes dropped]", b.dropped)
	}
	return s
}

// runCommandBounded runs cmd with a bounded lifetime and bounded captured
// output, and always reaps the child.
//
// cmd MUST be built with exec.CommandContext so cancellation is wired up. The
// default CommandContext behaviour only signals the direct child, so a shell
// command that spawns a background grandchild leaves the grandchild holding the
// output pipe open and cmd.Wait blocks indefinitely. Cancel is therefore
// overridden to kill the whole process group, and WaitDelay bounds the wait for
// the output pipes after the kill.
func runCommandBounded(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &boundedBuffer{limit: maxCommandOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start command: %w", err)
	}

	waitDone := make(chan error, 1) // buffered: the waiter never blocks on exit
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		return out.String(), err
	case <-runCtx.Done():
		killProcessGroup(cmd)
		<-waitDone // Wait is always called, so pipes and the zombie are released
		if err := ctx.Err(); err != nil {
			return out.String(), err
		}
		return out.String(), fmt.Errorf("command timed out after %s", timeout)
	}
}
