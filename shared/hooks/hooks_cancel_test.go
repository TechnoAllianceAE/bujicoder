package hooks

import (
	"context"
	"testing"
	"time"
)

// Cancelling the run (user pressed Escape) must kill a running hook instead of
// waiting out its timeout, and must not start the hooks that follow it.
func TestRunHooksStopsOnCancelledContext(t *testing.T) {
	m := &Manager{hooks: []HookConfig{{
		Matcher: HookMatcher{Event: "PreToolUse"},
		Hooks: []HookEntry{
			{Type: "command", Command: "sleep 300", Timeout: 60000},
			{Type: "command", Command: "echo second", Timeout: 60000},
		},
	}}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	results := m.RunHooks(ctx, "PreToolUse", "write_file", nil)
	elapsed := time.Since(start)

	if elapsed > 15*time.Second {
		t.Fatalf("hooks ran for %v after cancellation; they must stop promptly", elapsed)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: hooks after the cancellation must not start", len(results))
	}
	if results[0].ExitCode == 0 {
		t.Error("a cancelled hook must not report success")
	}
	if results[0].Stderr == "" {
		t.Error("a cancelled hook must report why it failed")
	}
}

func TestRunHooksSkipsEverythingWhenAlreadyCancelled(t *testing.T) {
	m := &Manager{hooks: []HookConfig{{
		Matcher: HookMatcher{Event: "PreToolUse"},
		Hooks:   []HookEntry{{Type: "command", Command: "echo hi"}},
	}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if results := m.RunHooks(ctx, "PreToolUse", "write_file", nil); len(results) != 0 {
		t.Fatalf("got %d results, want 0 for an already-cancelled context", len(results))
	}
}
