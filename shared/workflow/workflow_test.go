package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// --- Condition tests ---

func TestEvaluateCondition_Empty(t *testing.T) {
	if !EvaluateCondition("", nil) {
		t.Error("empty condition should return true")
	}
}

func TestEvaluateCondition_Contains(t *testing.T) {
	vars := map[string]string{"review": "Code NEEDS_CHANGES in auth module"}

	if !EvaluateCondition("{{review}} contains 'NEEDS_CHANGES'", vars) {
		t.Error("expected true for contains match")
	}
	if EvaluateCondition("{{review}} contains 'PERFECT'", vars) {
		t.Error("expected false for contains mismatch")
	}
}

func TestEvaluateCondition_Equals(t *testing.T) {
	vars := map[string]string{"status": "approved"}

	if !EvaluateCondition("{{status}} equals 'approved'", vars) {
		t.Error("expected true for equals match")
	}
	if EvaluateCondition("{{status}} equals 'denied'", vars) {
		t.Error("expected false for equals mismatch")
	}
}

func TestEvaluateCondition_NotEmpty(t *testing.T) {
	vars := map[string]string{"result": "something"}

	if !EvaluateCondition("{{result}} not_empty", vars) {
		t.Error("expected true for not_empty with value")
	}

	vars["result"] = ""
	if EvaluateCondition("{{result}} not_empty", vars) {
		t.Error("expected false for not_empty with empty value")
	}
}

func TestEvaluateCondition_Empty_Operator(t *testing.T) {
	vars := map[string]string{"result": ""}

	if !EvaluateCondition("{{result}} empty", vars) {
		t.Error("expected true for empty with empty value")
	}

	vars["result"] = "has content"
	if EvaluateCondition("{{result}} empty", vars) {
		t.Error("expected false for empty with non-empty value")
	}
}

func TestEvaluateCondition_UnknownOperator(t *testing.T) {
	// Unknown operators default to true
	if !EvaluateCondition("some random text", nil) {
		t.Error("unknown condition format should default to true")
	}
}

// --- Interpolation tests ---

func TestInterpolate_NoPlaceholders(t *testing.T) {
	result := Interpolate("hello world", nil)
	if result != "hello world" {
		t.Errorf("expected unchanged text, got: %s", result)
	}
}

func TestInterpolate_SingleVar(t *testing.T) {
	vars := map[string]string{"name": "Alice"}
	result := Interpolate("Hello {{name}}", vars)
	if result != "Hello Alice" {
		t.Errorf("expected 'Hello Alice', got: %s", result)
	}
}

func TestInterpolate_MultipleVars(t *testing.T) {
	vars := map[string]string{"action": "fix", "file": "main.go"}
	result := Interpolate("{{action}} the bug in {{file}}", vars)
	if result != "fix the bug in main.go" {
		t.Errorf("expected interpolated text, got: %s", result)
	}
}

func TestInterpolate_UnknownVarLeftAsIs(t *testing.T) {
	vars := map[string]string{"known": "value"}
	result := Interpolate("{{known}} and {{unknown}}", vars)
	if result != "value and {{unknown}}" {
		t.Errorf("expected unknown var to remain, got: %s", result)
	}
}

// --- Workflow validation tests ---

func TestWorkflow_Validate_MissingID(t *testing.T) {
	wf := Workflow{Steps: []Step{{Agent: "test"}}}
	if err := wf.Validate(); err == nil {
		t.Error("expected error for missing ID")
	}
}

func TestWorkflow_Validate_NoSteps(t *testing.T) {
	wf := Workflow{ID: "test"}
	if err := wf.Validate(); err == nil {
		t.Error("expected error for no steps")
	}
}

func TestWorkflow_Validate_MissingAgent(t *testing.T) {
	wf := Workflow{
		ID:    "test",
		Steps: []Step{{Task: "do something"}},
	}
	if err := wf.Validate(); err == nil {
		t.Error("expected error for missing agent")
	}
}

func TestWorkflow_Validate_ParallelMissingAgent(t *testing.T) {
	wf := Workflow{
		ID: "test",
		Steps: []Step{{
			Parallel: []Step{
				{Agent: "a"},
				{Task: "missing agent"},
			},
		}},
	}
	if err := wf.Validate(); err == nil {
		t.Error("expected error for parallel step missing agent")
	}
}

func TestWorkflow_Validate_Valid(t *testing.T) {
	wf := Workflow{
		ID:          "review",
		DisplayName: "Code Review",
		Steps: []Step{
			{Agent: "researcher", Task: "analyze code"},
			{Agent: "reviewer", Task: "review changes"},
		},
	}
	if err := wf.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestWorkflow_Validate_ValidParallel(t *testing.T) {
	wf := Workflow{
		ID: "parallel-test",
		Steps: []Step{{
			Parallel: []Step{
				{Agent: "a", Task: "task 1"},
				{Agent: "b", Task: "task 2"},
			},
		}},
	}
	if err := wf.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// --- Registry tests ---

func TestRegistry_Basic(t *testing.T) {
	reg := NewRegistry()

	wf := &Workflow{
		ID:          "test",
		DisplayName: "Test Workflow",
		Steps:       []Step{{Agent: "a", Task: "do it"}},
	}
	reg.Register(wf)

	got, ok := reg.Get("test")
	if !ok {
		t.Fatal("expected workflow to be found")
	}
	if got.DisplayName != "Test Workflow" {
		t.Errorf("expected 'Test Workflow', got: %s", got.DisplayName)
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("expected nonexistent workflow to not be found")
	}
}

func TestRegistry_List(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&Workflow{ID: "c", Steps: []Step{{Agent: "a"}}})
	reg.Register(&Workflow{ID: "a", Steps: []Step{{Agent: "a"}}})
	reg.Register(&Workflow{ID: "b", Steps: []Step{{Agent: "a"}}})

	ids := reg.List()
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(ids))
	}
	// Should be sorted
	if ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Errorf("expected sorted IDs, got: %v", ids)
	}
}

func TestRegistry_LoadBytes(t *testing.T) {
	yaml := `
id: deploy
display_name: Deploy Pipeline
description: Build, test, and deploy
steps:
  - agent: builder
    task: "Build the project"
    output_var: build_result
  - agent: tester
    task: "Test with: {{build_result}}"
`
	reg := NewRegistry()
	if err := reg.LoadBytes([]byte(yaml), "test.yaml"); err != nil {
		t.Fatalf("LoadBytes failed: %v", err)
	}

	wf, ok := reg.Get("deploy")
	if !ok {
		t.Fatal("expected deploy workflow to be loaded")
	}
	if wf.DisplayName != "Deploy Pipeline" {
		t.Errorf("expected 'Deploy Pipeline', got: %s", wf.DisplayName)
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(wf.Steps))
	}
	if wf.Steps[0].OutputVar != "build_result" {
		t.Errorf("expected output_var 'build_result', got: %s", wf.Steps[0].OutputVar)
	}
}

func TestRegistry_LoadBytes_Invalid(t *testing.T) {
	reg := NewRegistry()
	err := reg.LoadBytes([]byte("not: valid: yaml: ["), "bad.yaml")
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestRegistry_LoadBytes_ValidationError(t *testing.T) {
	yaml := `
id: ""
steps: []
`
	reg := NewRegistry()
	err := reg.LoadBytes([]byte(yaml), "bad.yaml")
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestRegistry_ListWorkflows(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&Workflow{ID: "z", DisplayName: "Z", Steps: []Step{{Agent: "a"}}})
	reg.Register(&Workflow{ID: "a", DisplayName: "A", Steps: []Step{{Agent: "a"}}})

	wfs := reg.ListWorkflows()
	if len(wfs) != 2 {
		t.Fatalf("expected 2, got %d", len(wfs))
	}
	if wfs[0].ID != "a" || wfs[1].ID != "z" {
		t.Errorf("expected sorted by ID, got: %s, %s", wfs[0].ID, wfs[1].ID)
	}
}

// --- Engine tests ---

// mockRunner is a simple AgentRunner for testing.
type mockRunner struct {
	responses map[string]string // agentID → output
	mu        sync.Mutex
	calls     []string // track agent calls in order (protected by mu)
	callCount int32
}

func (m *mockRunner) RunAgent(_ context.Context, agentID, task string) (string, error) {
	atomic.AddInt32(&m.callCount, 1)
	m.mu.Lock()
	m.calls = append(m.calls, agentID)
	m.mu.Unlock()
	if resp, ok := m.responses[agentID]; ok {
		return resp, nil
	}
	return fmt.Sprintf("[%s] completed: %s", agentID, task), nil
}

func (m *mockRunner) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.calls))
	copy(cp, m.calls)
	return cp
}

func TestEngine_SimpleWorkflow(t *testing.T) {
	wf := &Workflow{
		ID: "simple",
		Steps: []Step{
			{Agent: "researcher", Task: "research {{user_task}}", OutputVar: "research"},
			{Agent: "editor", Task: "implement based on: {{research}}"},
		},
	}

	runner := &mockRunner{
		responses: map[string]string{
			"researcher": "Found: use goroutines",
		},
	}

	engine := NewEngine()
	cfg := EngineConfig{
		Runner:   runner,
		UserTask: "fix concurrency bug",
	}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := engine.GetVars()
	if vars["research"] != "Found: use goroutines" {
		t.Errorf("expected research var, got: %s", vars["research"])
	}
	if vars["user_task"] != "fix concurrency bug" {
		t.Errorf("expected user_task var, got: %s", vars["user_task"])
	}
}

func TestEngine_ConditionalSkip(t *testing.T) {
	wf := &Workflow{
		ID: "conditional",
		Steps: []Step{
			{Agent: "reviewer", Task: "review code", OutputVar: "review"},
			{
				Agent:     "fixer",
				Task:      "fix: {{review}}",
				Condition: "{{review}} contains 'NEEDS_FIX'",
			},
		},
	}

	runner := &mockRunner{
		responses: map[string]string{
			"reviewer": "Code looks PERFECT",
		},
	}

	var events []StepEvent
	engine := NewEngine()
	cfg := EngineConfig{
		Runner: runner,
		OnEvent: func(e StepEvent) {
			events = append(events, e)
		},
	}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// fixer should have been skipped
	skipFound := false
	for _, e := range events {
		if e.AgentID == "fixer" && e.Status == "skip" {
			skipFound = true
		}
	}
	if !skipFound {
		t.Error("expected fixer step to be skipped")
	}
}

func TestEngine_ConditionalRun(t *testing.T) {
	wf := &Workflow{
		ID: "conditional-run",
		Steps: []Step{
			{Agent: "reviewer", Task: "review code", OutputVar: "review"},
			{
				Agent:     "fixer",
				Task:      "fix: {{review}}",
				Condition: "{{review}} contains 'NEEDS_FIX'",
			},
		},
	}

	runner := &mockRunner{
		responses: map[string]string{
			"reviewer": "Code NEEDS_FIX in module X",
		},
	}

	engine := NewEngine()
	cfg := EngineConfig{Runner: runner}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := runner.getCalls()
	if len(calls) != 2 {
		t.Errorf("expected 2 calls, got %d: %v", len(calls), calls)
	}
}

func TestEngine_ParallelSteps(t *testing.T) {
	wf := &Workflow{
		ID: "parallel",
		Steps: []Step{
			{
				Parallel: []Step{
					{Agent: "analyzer-a", Task: "analyze A", OutputVar: "result_a"},
					{Agent: "analyzer-b", Task: "analyze B", OutputVar: "result_b"},
				},
			},
			{Agent: "combiner", Task: "combine {{result_a}} and {{result_b}}"},
		},
	}

	runner := &mockRunner{
		responses: map[string]string{
			"analyzer-a": "Result from A",
			"analyzer-b": "Result from B",
		},
	}

	engine := NewEngine()
	cfg := EngineConfig{Runner: runner}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := engine.GetVars()
	if vars["result_a"] != "Result from A" {
		t.Errorf("expected result_a, got: %s", vars["result_a"])
	}
	if vars["result_b"] != "Result from B" {
		t.Errorf("expected result_b, got: %s", vars["result_b"])
	}
}

func TestEngine_ApprovalDenied(t *testing.T) {
	wf := &Workflow{
		ID: "approval",
		Steps: []Step{
			{Agent: "deployer", Task: "deploy to prod", RequireApproval: true},
		},
	}

	runner := &mockRunner{}
	engine := NewEngine()
	cfg := EngineConfig{
		Runner: runner,
		ApprovalFn: func(desc string) (bool, error) {
			return false, nil // deny
		},
	}

	err := engine.Execute(context.Background(), wf, cfg)
	if err == nil {
		t.Fatal("expected error for denied approval")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected denial error, got: %v", err)
	}
}

func TestEngine_ApprovalApproved(t *testing.T) {
	wf := &Workflow{
		ID: "approval",
		Steps: []Step{
			{Agent: "deployer", Task: "deploy to prod", RequireApproval: true},
		},
	}

	runner := &mockRunner{
		responses: map[string]string{"deployer": "deployed!"},
	}
	engine := NewEngine()
	cfg := EngineConfig{
		Runner: runner,
		ApprovalFn: func(desc string) (bool, error) {
			return true, nil // approve
		},
	}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEngine_InitialVars(t *testing.T) {
	wf := &Workflow{
		ID: "vars",
		Steps: []Step{
			{Agent: "worker", Task: "work on {{branch}} in {{repo}}"},
		},
	}

	runner := &mockRunner{}
	engine := NewEngine()
	cfg := EngineConfig{
		Runner: runner,
		InitialVars: map[string]string{
			"branch": "main",
			"repo":   "bujicoder",
		},
	}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vars := engine.GetVars()
	if vars["branch"] != "main" {
		t.Errorf("expected 'main', got: %s", vars["branch"])
	}
}

func TestEngine_NilRunner(t *testing.T) {
	wf := &Workflow{ID: "test", Steps: []Step{{Agent: "a"}}}
	engine := NewEngine()
	cfg := EngineConfig{} // no runner

	err := engine.Execute(context.Background(), wf, cfg)
	if err == nil {
		t.Error("expected error for nil runner")
	}
}

func TestEngine_ContextCancellation(t *testing.T) {
	wf := &Workflow{
		ID: "cancel",
		Steps: []Step{
			{Agent: "slow", Task: "do something slow"},
			{Agent: "never", Task: "should not run"},
		},
	}

	// Use an already-cancelled context so the first select{} check triggers.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := &mockRunner{
		responses: map[string]string{},
	}

	engine := NewEngine()
	cfg := EngineConfig{Runner: runner}

	err := engine.Execute(ctx, wf, cfg)
	if err == nil {
		t.Error("expected error from context cancellation")
	}
}

func TestEngine_Events(t *testing.T) {
	wf := &Workflow{
		ID: "events",
		Steps: []Step{
			{Agent: "worker", Task: "do work", OutputVar: "output"},
		},
	}

	runner := &mockRunner{
		responses: map[string]string{"worker": "done"},
	}

	var events []StepEvent
	engine := NewEngine()
	cfg := EngineConfig{
		Runner: runner,
		OnEvent: func(e StepEvent) {
			events = append(events, e)
		},
	}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have start and complete events
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	hasStart := false
	hasComplete := false
	for _, e := range events {
		if e.Status == "start" {
			hasStart = true
		}
		if e.Status == "complete" {
			hasComplete = true
		}
	}
	if !hasStart || !hasComplete {
		t.Errorf("expected start and complete events, got: %v", events)
	}
}

func TestEngine_GetVars_ReturnsCopy(t *testing.T) {
	engine := NewEngine()
	engine.vars["key"] = "value"

	v := engine.GetVars()
	v["key"] = "modified"

	if engine.vars["key"] != "value" {
		t.Error("GetVars should return a copy, not a reference")
	}
}

// --- truncateTask tests ---

func TestTruncateTask(t *testing.T) {
	short := "short task"
	if truncateTask(short, 100) != short {
		t.Error("short task should not be truncated")
	}

	long := strings.Repeat("a", 200)
	result := truncateTask(long, 50)
	if len(result) != 50 {
		t.Errorf("expected length 50, got %d", len(result))
	}
	if !strings.HasSuffix(result, "...") {
		t.Error("expected truncation suffix '...'")
	}
}

func TestTruncateTask_Newlines(t *testing.T) {
	task := "line1\nline2\nline3"
	result := truncateTask(task, 100)
	if strings.Contains(result, "\n") {
		t.Error("newlines should be replaced with spaces")
	}
}

// --- Registry LoadDir with nonexistent directory ---

func TestRegistry_LoadDir_Nonexistent(t *testing.T) {
	reg := NewRegistry()
	err := reg.LoadDir("/nonexistent/path/to/workflows")
	if err != nil {
		t.Errorf("expected nil error for nonexistent dir, got: %v", err)
	}
}

// --- Parallel with conditional skip ---

func TestEngine_ParallelWithConditionalSkip(t *testing.T) {
	wf := &Workflow{
		ID: "parallel-cond",
		Steps: []Step{
			{
				Parallel: []Step{
					{Agent: "always", Task: "runs always", OutputVar: "a"},
					{Agent: "conditional", Task: "skipped", Condition: "{{flag}} equals 'run'"},
				},
			},
		},
	}

	runner := &mockRunner{
		responses: map[string]string{"always": "ok"},
	}

	var mu sync.Mutex
	var events []StepEvent
	engine := NewEngine()
	cfg := EngineConfig{
		Runner: runner,
		InitialVars: map[string]string{
			"flag": "skip",
		},
		OnEvent: func(e StepEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
	}

	err := engine.Execute(context.Background(), wf, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have a skip event for the conditional step
	mu.Lock()
	defer mu.Unlock()
	skipFound := false
	for _, e := range events {
		if e.AgentID == "conditional" && e.Status == "skip" {
			skipFound = true
		}
	}
	if !skipFound {
		t.Error("expected conditional parallel step to be skipped")
	}
}
