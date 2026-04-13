package hooks

import (
	"testing"
)

func TestNormalizeToolName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Bash", "run_terminal_command"},
		{"PowerShell", "run_terminal_command"},
		{"Write", "write_file"},
		{"Edit", "str_replace"},
		{"Read", "read_files"},
		{"Glob", "glob"},
		{"Grep", "code_search"},
		// Already buji-style names pass through
		{"run_terminal_command", "run_terminal_command"},
		{"write_file", "write_file"},
		{"str_replace", "str_replace"},
		{"spawn_agents", "spawn_agents"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeToolName(tt.input)
			if got != tt.want {
				t.Errorf("normalizeToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMatchesEvent(t *testing.T) {
	tests := []struct {
		name     string
		matcher  HookMatcher
		event    string
		toolName string
		want     bool
	}{
		{"exact match", HookMatcher{Event: "PreToolUse", ToolName: "write_file"}, "PreToolUse", "write_file", true},
		{"event only", HookMatcher{Event: "PreToolUse"}, "PreToolUse", "anything", true},
		{"wrong event", HookMatcher{Event: "PostToolUse"}, "PreToolUse", "write_file", false},
		{"wrong tool", HookMatcher{Event: "PreToolUse", ToolName: "read_files"}, "PreToolUse", "write_file", false},
		{"case insensitive event", HookMatcher{Event: "pretooluse"}, "PreToolUse", "write_file", true},
		{"bc2 tool name in matcher", HookMatcher{Event: "PreToolUse", ToolName: "Bash"}, "PreToolUse", "run_terminal_command", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesEvent(tt.matcher, tt.event, tt.toolName)
			if got != tt.want {
				t.Errorf("matchesEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewManager_NoFiles(t *testing.T) {
	m := NewManager("/nonexistent/path.json")
	if m.HasHooks() {
		t.Error("should have no hooks from nonexistent file")
	}
}

func TestRunHooks_Empty(t *testing.T) {
	m := &Manager{}
	results := m.RunHooks("PreToolUse", "write_file", nil)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}
