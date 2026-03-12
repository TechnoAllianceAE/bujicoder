package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func todoCtx() context.Context {
	return WithTodoList(context.Background(), NewTodoList())
}

func TestTodoWrite_Basic(t *testing.T) {
	fn := todoWrite()
	args := `{
		"items": [
			{"id": "1", "task": "Read the codebase", "status": "done"},
			{"id": "2", "task": "Write the tests", "status": "in_progress"},
			{"id": "3", "task": "Deploy to prod", "status": "pending"}
		]
	}`

	result, err := fn(todoCtx(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("todoWrite: %v", err)
	}
	if !strings.Contains(result, "3 items") {
		t.Errorf("expected 3 items in result, got:\n%s", result)
	}
	if !strings.Contains(result, "[x] Read the codebase") {
		t.Error("done item should show [x]")
	}
	if !strings.Contains(result, "[~] Write the tests") {
		t.Error("in_progress item should show [~]")
	}
	if !strings.Contains(result, "[ ] Deploy to prod") {
		t.Error("pending item should show [ ]")
	}
}

func TestTodoWrite_WithNote(t *testing.T) {
	fn := todoWrite()
	args := `{
		"items": [
			{"id": "1", "task": "Fix bug", "status": "blocked", "note": "waiting for API docs"}
		]
	}`

	result, err := fn(todoCtx(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("todoWrite: %v", err)
	}
	if !strings.Contains(result, "[!] Fix bug") {
		t.Error("blocked item should show [!]")
	}
	if !strings.Contains(result, "waiting for API docs") {
		t.Error("note should be included")
	}
}

func TestTodoWrite_Clear(t *testing.T) {
	fn := todoWrite()
	args := `{"items": []}`

	result, err := fn(todoCtx(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("todoWrite: %v", err)
	}
	if result != "Todo list cleared." {
		t.Errorf("expected clear message, got: %q", result)
	}
}

func TestTodoWrite_InvalidStatus(t *testing.T) {
	fn := todoWrite()
	args := `{
		"items": [
			{"id": "1", "task": "Test", "status": "invalid_status"}
		]
	}`

	_, err := fn(todoCtx(), json.RawMessage(args))
	if err == nil {
		t.Error("expected error for invalid status")
	}
}

func TestTodoWrite_DefaultStatus(t *testing.T) {
	fn := todoWrite()
	args := `{
		"items": [
			{"id": "1", "task": "No status given"}
		]
	}`

	result, err := fn(todoCtx(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("todoWrite: %v", err)
	}
	if !strings.Contains(result, "[ ] No status given") {
		t.Errorf("empty status should default to pending, got:\n%s", result)
	}
}

func TestTodoRead_Empty(t *testing.T) {
	fn := todoRead()
	result, err := fn(todoCtx(), nil)
	if err != nil {
		t.Fatalf("todoRead: %v", err)
	}
	if result != "No todos set." {
		t.Errorf("expected empty message, got: %q", result)
	}
}

func TestTodoRead_AfterWrite(t *testing.T) {
	ctx := todoCtx()
	writeFn := todoWrite()
	readFn := todoRead()

	// Write some todos.
	args := `{
		"items": [
			{"id": "1", "task": "Task A", "status": "pending"},
			{"id": "2", "task": "Task B", "status": "done"}
		]
	}`
	_, err := writeFn(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("todoWrite: %v", err)
	}

	// Read them back.
	result, err := readFn(ctx, nil)
	if err != nil {
		t.Fatalf("todoRead: %v", err)
	}

	// Should be valid JSON.
	var items []TodoItem
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		t.Fatalf("result is not valid JSON: %v\nresult: %s", err, result)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Task != "Task A" {
		t.Errorf("items[0].Task = %q, want Task A", items[0].Task)
	}
	if items[1].Status != "done" {
		t.Errorf("items[1].Status = %q, want done", items[1].Status)
	}
}

func TestTodoWrite_NoContext(t *testing.T) {
	fn := todoWrite()
	_, err := fn(context.Background(), json.RawMessage(`{"items":[]}`))
	if err == nil {
		t.Error("expected error without todo list in context")
	}
}

func TestTodoRead_NoContext(t *testing.T) {
	fn := todoRead()
	_, err := fn(context.Background(), nil)
	if err == nil {
		t.Error("expected error without todo list in context")
	}
}
