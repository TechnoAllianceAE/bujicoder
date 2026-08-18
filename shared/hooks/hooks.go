// Package hooks implements pre/post tool execution lifecycle hooks.
// Hooks are shell commands configured in settings.json that fire before
// or after tool calls, enabling users to block, observe, or modify behavior.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// defaultHookTimeout bounds a hook that does not set its own timeout.
	defaultHookTimeout = 30 * time.Second

	// waitDelay caps how long cmd.Wait tolerates a descendant that inherited the
	// hook's output pipes after the hook itself was killed.
	waitDelay = 5 * time.Second

	// maxHookOutput caps how much stdout/stderr is retained per hook.
	maxHookOutput = 64 << 10
)

// boundedBuffer collects at most limit bytes and reports every write as
// successful, so a hook is never killed by a short write.
type boundedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) <= room {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	if b.truncated {
		return string(b.buf) + "\n... (output truncated)"
	}
	return string(b.buf)
}

// HookConfig ties a matcher to one or more hook entries.
type HookConfig struct {
	Matcher HookMatcher `json:"matcher"`
	Hooks   []HookEntry `json:"hooks"`
}

// HookMatcher selects which events trigger the hooks.
type HookMatcher struct {
	Event    string `json:"event"`    // "PreToolUse", "PostToolUse"
	ToolName string `json:"toolName"` // optional: restrict to this tool
}

// HookEntry describes a single hook to run.
type HookEntry struct {
	Type    string `json:"type"`    // "command"
	Command string `json:"command"` // shell command
	Timeout int    `json:"timeout"` // milliseconds (0 = 30s default)
}

// HookResult contains the outcome of a hook execution.
type HookResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Blocked  bool   // true when exit code == 2
	Message  string // user-facing message when blocked
}

// Manager loads and executes hooks from settings files.
type Manager struct {
	hooks []HookConfig
}

// NewManager creates a hook manager by loading hooks from the given config paths.
// Each path should point to a settings.json file. Missing files are silently skipped.
func NewManager(configPaths ...string) *Manager {
	m := &Manager{}
	for _, p := range configPaths {
		m.loadFromFile(p)
	}
	return m
}

func (m *Manager) loadFromFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var settings struct {
		Hooks []HookConfig `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return
	}

	m.hooks = append(m.hooks, settings.Hooks...)
}

// MergeHooks adds hook configs from an external source (e.g. plugins).
func (m *Manager) MergeHooks(hooks []HookConfig) {
	m.hooks = append(m.hooks, hooks...)
}

// RunHooks executes all matching hooks for the given event. Returns results
// in order. If any hook returns exit code 2, its Blocked flag is set.
//
// ctx bounds the whole batch: each hook's own timeout is derived from it, so
// cancelling the run (user pressed Escape) kills the running hook's process
// group instead of waiting out its timeout, and no further hooks are started.
func (m *Manager) RunHooks(ctx context.Context, event, toolName string, input map[string]any) []HookResult {
	if len(m.hooks) == 0 {
		return nil
	}

	// Normalize tool name for matching (bc2 "Bash" → buji "run_terminal_command")
	normalizedName := normalizeToolName(toolName)

	var results []HookResult
	for _, hc := range m.hooks {
		if !matchesEvent(hc.Matcher, event, normalizedName) {
			continue
		}
		for _, entry := range hc.Hooks {
			if entry.Type != "command" {
				continue
			}
			if ctx.Err() != nil {
				return results
			}
			results = append(results, executeHook(ctx, entry, event, toolName, input))
		}
	}
	return results
}

// HasHooks returns true if any hooks are configured.
func (m *Manager) HasHooks() bool {
	return len(m.hooks) > 0
}

func matchesEvent(matcher HookMatcher, event, toolName string) bool {
	if !strings.EqualFold(matcher.Event, event) {
		return false
	}
	if matcher.ToolName != "" {
		normalized := normalizeToolName(matcher.ToolName)
		if normalized != toolName {
			return false
		}
	}
	return true
}

func executeHook(parent context.Context, entry HookEntry, event, toolName string, input map[string]any) HookResult {
	timeout := time.Duration(entry.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", entry.Command)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", entry.Command)
	}
	// A hook command routinely spawns children. Put it in its own process group
	// and kill the group on timeout: killing only the shell leaves the children
	// running and holding the output pipes, which makes cmd.Wait block well past
	// the hook's timeout and stalls the tool call that triggered it.
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = waitDelay

	// Pass tool input as JSON via stdin.
	inputJSON, err := json.Marshal(input)
	if err != nil {
		// Never feed the hook a half-encoded payload.
		inputJSON = []byte("{}")
	}
	cmd.Stdin = bytes.NewReader(inputJSON)

	// Set environment variables
	cmd.Env = append(os.Environ(),
		"BUJI_HOOK_EVENT="+event,
		"BUJI_TOOL_NAME="+toolName,
	)

	// Bounded buffers: a hook that floods stdout would otherwise be buffered in
	// full and then handed to the model.
	stdout := &boundedBuffer{limit: maxHookOutput}
	stderr := &boundedBuffer{limit: maxHookOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()

	result := HookResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		// A hook that could not start, timed out, or was killed by a signal has
		// no useful exit status; report a generic failure rather than 0 or -1.
		if result.ExitCode <= 0 {
			result.ExitCode = 1
		}
		// The reason must reach the user: a silently killed hook is invisible.
		if result.Stderr == "" {
			switch {
			case parent.Err() != nil:
				result.Stderr = fmt.Sprintf("hook cancelled: %v", runErr)
			case ctx.Err() != nil:
				result.Stderr = fmt.Sprintf("hook timed out after %v: %v", timeout, runErr)
			default:
				result.Stderr = runErr.Error()
			}
		}
	}

	// Exit code 2 = block the operation
	if result.ExitCode == 2 {
		result.Blocked = true
		result.Message = strings.TrimSpace(result.Stdout)
		if result.Message == "" {
			result.Message = fmt.Sprintf("Hook blocked tool call: %s", toolName)
		}
	}

	return result
}

// normalizeToolName maps bc2-style tool names to buji-style names so hook
// configs from either project work correctly.
func normalizeToolName(name string) string {
	switch name {
	case "Bash", "PowerShell":
		return "run_terminal_command"
	case "Write":
		return "write_file"
	case "Edit":
		return "str_replace"
	case "Read":
		return "read_files"
	case "Glob":
		return "glob"
	case "Grep":
		return "code_search"
	case "WebFetch":
		return "web_fetch"
	case "WebSearch":
		return "web_search"
	default:
		return name
	}
}

// NewManagerFromConfigDir creates a Manager by loading hooks from the standard
// config locations (~/.bujicoder/settings.json and .bujicoder/settings.json).
func NewManagerFromConfigDir(configDir, projectRoot string) *Manager {
	paths := []string{
		filepath.Join(configDir, "settings.json"),
	}
	if projectRoot != "" {
		paths = append(paths, filepath.Join(projectRoot, ".bujicoder", "settings.json"))
	}
	return NewManager(paths...)
}
