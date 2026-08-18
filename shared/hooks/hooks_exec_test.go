//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecuteHookExitCodes(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		wantExit    int
		wantBlocked bool
		wantStdout  string
	}{
		{name: "success", command: "echo ok", wantExit: 0, wantStdout: "ok\n"},
		{name: "failure", command: "exit 3", wantExit: 3},
		{name: "block", command: "echo denied; exit 2", wantExit: 2, wantBlocked: true, wantStdout: "denied\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := executeHook(t.Context(), HookEntry{Type: "command", Command: tc.command}, "PreToolUse", "write_file", nil)
			if r.ExitCode != tc.wantExit {
				t.Fatalf("ExitCode = %d, want %d (stderr=%q)", r.ExitCode, tc.wantExit, r.Stderr)
			}
			if r.Blocked != tc.wantBlocked {
				t.Fatalf("Blocked = %v, want %v", r.Blocked, tc.wantBlocked)
			}
			if tc.wantStdout != "" && r.Stdout != tc.wantStdout {
				t.Fatalf("Stdout = %q, want %q", r.Stdout, tc.wantStdout)
			}
		})
	}
}

// A hook that leaves a child holding the output pipes must not stall the tool
// call past its timeout, and the child must be cleaned up.
func TestExecuteHookTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	entry := HookEntry{
		Type:    "command",
		Command: "sleep 300 & echo $! > " + pidFile + "; sleep 300",
		Timeout: 500, // ms
	}

	start := time.Now()
	r := executeHook(t.Context(), entry, "PreToolUse", "write_file", nil)
	elapsed := time.Since(start)

	if elapsed > 20*time.Second {
		t.Fatalf("hook blocked for %v past its 500ms timeout", elapsed)
	}
	if r.ExitCode == 0 {
		t.Fatal("a hook killed by its timeout must not report success")
	}
	if r.Stderr == "" {
		t.Fatal("a timed-out hook must report why it failed")
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Skipf("shell did not record a child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Skipf("bad pid %q", raw)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return // child reaped
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d survived the hook timeout: orphaned process", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExecuteHookBoundsOutput(t *testing.T) {
	entry := HookEntry{
		Type:    "command",
		Command: "head -c 500000 /dev/zero | tr '\\0' 'a'",
	}
	r := executeHook(t.Context(), entry, "PreToolUse", "write_file", nil)
	if len(r.Stdout) > maxHookOutput+64 {
		t.Fatalf("captured %d bytes of hook output; want at most %d", len(r.Stdout), maxHookOutput)
	}
	if !strings.Contains(r.Stdout, "truncated") {
		t.Fatal("truncation was not reported to the caller")
	}
}

// A failing hook must not wedge the flow: RunHooks reports every result and
// returns.
func TestRunHooksSurvivesFailingHook(t *testing.T) {
	m := &Manager{hooks: []HookConfig{{
		Matcher: HookMatcher{Event: "PreToolUse"},
		Hooks: []HookEntry{
			{Type: "command", Command: "exit 1"},
			{Type: "command", Command: "echo second"},
			{Type: "not-a-command", Command: "ignored"},
		},
	}}}

	results := m.RunHooks(t.Context(), "PreToolUse", "write_file", map[string]any{"path": "x"})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (non-command entries skipped)", len(results))
	}
	if results[0].ExitCode != 1 {
		t.Fatalf("first hook ExitCode = %d, want 1", results[0].ExitCode)
	}
	if strings.TrimSpace(results[1].Stdout) != "second" {
		t.Fatalf("second hook did not run after the first failed: %q", results[1].Stdout)
	}
}

func TestExecuteHookPassesInputAndEnv(t *testing.T) {
	entry := HookEntry{Type: "command", Command: `cat; printf "%s/%s" "$BUJI_HOOK_EVENT" "$BUJI_TOOL_NAME"`}
	r := executeHook(t.Context(), entry, "PreToolUse", "write_file", map[string]any{"path": "a.go"})
	if !strings.Contains(r.Stdout, `"path":"a.go"`) {
		t.Fatalf("hook stdin did not receive the tool input: %q", r.Stdout)
	}
	if !strings.Contains(r.Stdout, "PreToolUse/write_file") {
		t.Fatalf("hook env was not set: %q", r.Stdout)
	}
}

func TestBoundedBufferReportsFullWrites(t *testing.T) {
	b := &boundedBuffer{limit: 4}
	n, err := b.Write([]byte("abcdefgh"))
	if n != 8 || err != nil {
		t.Fatalf("Write = (%d, %v), want (8, nil): a short write would kill the hook", n, err)
	}
	if got := b.String(); !strings.HasPrefix(got, "abcd") || !strings.Contains(got, "truncated") {
		t.Fatalf("String() = %q", got)
	}
}
