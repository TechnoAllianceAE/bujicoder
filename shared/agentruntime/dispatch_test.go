package agentruntime

import (
	"testing"

	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

func TestIsSafeTool(t *testing.T) {
	safe := []string{"read_files", "list_directory", "glob", "find_files", "code_search", "web_search"}
	for _, name := range safe {
		if !isSafeTool(name) {
			t.Errorf("expected %q to be safe", name)
		}
	}

	unsafe := []string{
		"write_file", "str_replace", "run_terminal_command",
		"spawn_agents", "think_deeply", "apply_proposals",
		"ask_user", "propose_edit", "propose_write_file",
	}
	for _, name := range unsafe {
		if isSafeTool(name) {
			t.Errorf("expected %q to be unsafe", name)
		}
	}
}

func TestDispatchToolCalls_ParallelSafe(t *testing.T) {
	// Set up a runtime with real tool registry.
	provider := &testProvider{
		responses: [][]llm.StreamEvent{},
	}
	rt := setupRuntime(provider)

	agentDef := &agent.Definition{
		ID:       "test-agent",
		Model:    "test/model",
		MaxSteps: 10,
		Tools:    []string{"read_files", "list_directory"},
	}

	// Create multiple safe tool calls.
	toolCalls := []llm.ToolCallEvent{
		{ID: "call-1", Name: "read_files", ArgumentsJSON: `{"paths":["/tmp/nonexistent1.txt"]}`},
		{ID: "call-2", Name: "read_files", ArgumentsJSON: `{"paths":["/tmp/nonexistent2.txt"]}`},
		{ID: "call-3", Name: "list_directory", ArgumentsJSON: `{"path":"."}`},
	}

	var events []Event
	cfg := RunConfig{
		AgentDef:    agentDef,
		ProjectRoot: t.TempDir(),
		OnEvent: func(ev Event) {
			events = append(events, ev)
		},
	}

	results, err := dispatchToolCalls(t.Context(), rt, toolCalls, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Should get 3 results in order.
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify results are in the correct order.
	if results[0].ToolCallID != "call-1" {
		t.Errorf("result[0] ToolCallID = %q, want call-1", results[0].ToolCallID)
	}
	if results[1].ToolCallID != "call-2" {
		t.Errorf("result[1] ToolCallID = %q, want call-2", results[1].ToolCallID)
	}
	if results[2].ToolCallID != "call-3" {
		t.Errorf("result[2] ToolCallID = %q, want call-3", results[2].ToolCallID)
	}

	// Should have 3 events.
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestDispatchToolCalls_MixedSafeUnsafe(t *testing.T) {
	provider := &testProvider{
		responses: [][]llm.StreamEvent{},
	}
	rt := setupRuntime(provider)

	agentDef := &agent.Definition{
		ID:       "test-agent",
		Model:    "test/model",
		MaxSteps: 10,
		Tools:    []string{"read_files", "write_file"},
	}

	dir := t.TempDir()

	toolCalls := []llm.ToolCallEvent{
		{ID: "call-1", Name: "read_files", ArgumentsJSON: `{"paths":["test.txt"]}`},
		{ID: "call-2", Name: "read_files", ArgumentsJSON: `{"paths":["test2.txt"]}`},
		{ID: "call-3", Name: "write_file", ArgumentsJSON: `{"path":"new.txt","content":"hello"}`},
		{ID: "call-4", Name: "read_files", ArgumentsJSON: `{"paths":["test3.txt"]}`},
	}

	cfg := RunConfig{
		AgentDef:    agentDef,
		ProjectRoot: dir,
	}

	results, err := dispatchToolCalls(t.Context(), rt, toolCalls, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	// Order must be preserved.
	for i, tc := range toolCalls {
		if results[i].ToolCallID != tc.ID {
			t.Errorf("result[%d] ToolCallID = %q, want %q", i, results[i].ToolCallID, tc.ID)
		}
	}

	// call-3 (write_file) ran — we only care about order preservation here,
	// not write_file success (macOS /tmp symlinks can cause safePath issues in tests).
	if results[2].ToolName != "write_file" {
		t.Errorf("result[2] ToolName = %q, want write_file", results[2].ToolName)
	}
}
