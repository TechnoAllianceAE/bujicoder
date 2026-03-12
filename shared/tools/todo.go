package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// TodoItem represents a single task in the agent's todo list.
type TodoItem struct {
	ID     string `json:"id"`
	Task   string `json:"task"`
	Status string `json:"status"` // "pending", "in_progress", "done", "blocked"
	Note   string `json:"note,omitempty"`
}

// TodoList is a thread-safe in-memory todo list for a single conversation.
type TodoList struct {
	mu    sync.Mutex
	items []TodoItem
}

// NewTodoList creates an empty todo list.
func NewTodoList() *TodoList {
	return &TodoList{}
}

const todoCtxKey contextKey = "tools_todo_list"

// WithTodoList returns a child context carrying a todo list.
func WithTodoList(ctx context.Context, list *TodoList) context.Context {
	return context.WithValue(ctx, todoCtxKey, list)
}

// getTodoList returns the todo list from context.
func getTodoList(ctx context.Context) *TodoList {
	l, _ := ctx.Value(todoCtxKey).(*TodoList)
	return l
}

// statusIcon returns a visual indicator for a status.
func statusIcon(status string) string {
	switch status {
	case "pending":
		return "[ ]"
	case "in_progress":
		return "[~]"
	case "done":
		return "[x]"
	case "blocked":
		return "[!]"
	default:
		return "[?]"
	}
}

// todoWrite creates the todo_write tool function.
func todoWrite() func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Items []TodoItem `json:"items"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parse todo_write args: %w", err)
		}

		list := getTodoList(ctx)
		if list == nil {
			return "", fmt.Errorf("todo list not available in this context")
		}

		// Validate statuses.
		for i, item := range params.Items {
			switch item.Status {
			case "pending", "in_progress", "done", "blocked":
				// valid
			case "":
				params.Items[i].Status = "pending"
			default:
				return "", fmt.Errorf("invalid status %q for item %q; must be pending, in_progress, done, or blocked",
					item.Status, item.ID)
			}
		}

		list.mu.Lock()
		list.items = params.Items
		list.mu.Unlock()

		// Format the result.
		if len(params.Items) == 0 {
			return "Todo list cleared.", nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Todo list updated (%d items):\n", len(params.Items)))
		for _, item := range params.Items {
			sb.WriteString(fmt.Sprintf("  %s %s", statusIcon(item.Status), item.Task))
			if item.Note != "" {
				sb.WriteString(fmt.Sprintf(" — %s", item.Note))
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
}

// todoRead creates the todo_read tool function.
func todoRead() func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, _ json.RawMessage) (string, error) {
		list := getTodoList(ctx)
		if list == nil {
			return "", fmt.Errorf("todo list not available in this context")
		}

		list.mu.Lock()
		items := make([]TodoItem, len(list.items))
		copy(items, list.items)
		list.mu.Unlock()

		if len(items) == 0 {
			return "No todos set.", nil
		}

		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}
