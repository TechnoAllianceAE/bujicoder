package llm

import (
	"io"
	"strings"
	"testing"
)

func ptrStr(s string) *string { return &s }

func TestAnthropicBuildRequest_CachePlumbing(t *testing.T) {
	a := NewAnthropicProvider("test-key")

	t.Run("no cache flags keeps string system + bare tools", func(t *testing.T) {
		req := &CompletionRequest{
			Model:        "claude-sonnet-4",
			SystemPrompt: ptrStr("be helpful"),
			Messages:     []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}}},
			Tools: []ToolDefinition{
				{Name: "t1", Description: "d1", InputSchema: map[string]any{}},
				{Name: "t2", Description: "d2", InputSchema: map[string]any{}},
			},
		}
		body := a.buildRequest(req)
		if s, ok := body["system"].(string); !ok || s != "be helpful" {
			t.Fatalf("system: want plain string, got %T %v", body["system"], body["system"])
		}
		tools := body["tools"].([]map[string]any)
		for i, tool := range tools {
			if _, has := tool["cache_control"]; has {
				t.Fatalf("tool[%d] should not have cache_control", i)
			}
		}
	})

	t.Run("SystemCacheable switches system to array with cache_control", func(t *testing.T) {
		req := &CompletionRequest{
			Model:           "claude-sonnet-4",
			SystemPrompt:    ptrStr("be helpful"),
			SystemCacheable: true,
			Messages:        []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}}},
		}
		body := a.buildRequest(req)
		arr, ok := body["system"].([]map[string]any)
		if !ok || len(arr) != 1 {
			t.Fatalf("system: want 1-elem array, got %T %v", body["system"], body["system"])
		}
		if arr[0]["text"] != "be helpful" {
			t.Fatalf("system text: want 'be helpful', got %v", arr[0]["text"])
		}
		cc, ok := arr[0]["cache_control"].(map[string]any)
		if !ok || cc["type"] != "ephemeral" {
			t.Fatalf("cache_control: want {type: ephemeral}, got %v", arr[0]["cache_control"])
		}
	})

	t.Run("ToolsCacheable attaches marker only to last tool", func(t *testing.T) {
		req := &CompletionRequest{
			Model:          "claude-sonnet-4",
			SystemPrompt:   ptrStr("x"),
			ToolsCacheable: true,
			Messages:       []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}}},
			Tools: []ToolDefinition{
				{Name: "t1", Description: "d1", InputSchema: map[string]any{}},
				{Name: "t2", Description: "d2", InputSchema: map[string]any{}},
			},
		}
		body := a.buildRequest(req)
		tools := body["tools"].([]map[string]any)
		if _, has := tools[0]["cache_control"]; has {
			t.Fatalf("first tool should not have cache_control")
		}
		if _, has := tools[1]["cache_control"]; !has {
			t.Fatalf("last tool should have cache_control")
		}
	})

	t.Run("CacheBreakpoint on message forces array form + marker on last block", func(t *testing.T) {
		req := &CompletionRequest{
			Model: "claude-sonnet-4",
			Messages: []Message{
				{Role: "user", Content: []ContentPart{{Type: "text", Text: "turn 1"}}, CacheBreakpoint: true},
				{Role: "assistant", Content: []ContentPart{{Type: "text", Text: "reply"}}},
				{Role: "user", Content: []ContentPart{{Type: "text", Text: "turn 2"}}},
			},
		}
		body := a.buildRequest(req)
		messages := body["messages"].([]map[string]any)
		// Message 0 has breakpoint → content is []map, last block carries marker.
		content0, ok := messages[0]["content"].([]map[string]any)
		if !ok {
			t.Fatalf("msg[0] content: want array form for breakpoint, got %T", messages[0]["content"])
		}
		if _, has := content0[len(content0)-1]["cache_control"]; !has {
			t.Fatalf("msg[0] last block should carry cache_control")
		}
		// Messages 1 and 2 keep string form (fast path, no breakpoint).
		if _, ok := messages[1]["content"].(string); !ok {
			t.Fatalf("msg[1] should keep string content form")
		}
		if _, ok := messages[2]["content"].(string); !ok {
			t.Fatalf("msg[2] should keep string content form")
		}
	})
}

func TestAnthropicProcessStream_CacheTokens(t *testing.T) {
	// A minimal SSE stream containing cache_read / cache_creation counters.
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":120,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		``,
	}, "\n")

	a := NewAnthropicProvider("test-key")
	ch := make(chan StreamEvent, 16)
	a.processStream(io.NopCloser(strings.NewReader(sse)), ch)

	var got *CompleteEvent
	for ev := range ch {
		if ev.Complete != nil {
			got = ev.Complete
		}
	}
	if got == nil {
		t.Fatal("expected a Complete event")
	}
	if got.Usage.InputTokens != 120 {
		t.Fatalf("input_tokens: want 120, got %d", got.Usage.InputTokens)
	}
	if got.Usage.OutputTokens != 7 {
		t.Fatalf("output_tokens: want 7, got %d", got.Usage.OutputTokens)
	}
	if got.Usage.CacheReadTokens != 100 {
		t.Fatalf("cache_read_tokens: want 100, got %d", got.Usage.CacheReadTokens)
	}
	if got.Usage.CacheWriteTokens != 20 {
		t.Fatalf("cache_write_tokens: want 20, got %d", got.Usage.CacheWriteTokens)
	}
}
