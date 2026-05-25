package llm

import "testing"

// buildRequest must translate the gateway's canonical OpenAI-style tool results
// (separate role:"tool" messages) into Anthropic wire format: tool_result blocks
// inside a role:"user" message. DeepSeek's /anthropic endpoint rejects role:"tool"
// with: unknown variant `tool`, expected `user` or `assistant`.

func messagesOf(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["messages"].([]map[string]any)
	if !ok {
		t.Fatalf("messages not []map[string]any, got %T", body["messages"])
	}
	return raw
}

func TestBuildRequest_ToolRoleBecomesUser(t *testing.T) {
	p := NewAnthropicProvider("k")
	maxTok := 1
	req := &CompletionRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: &maxTok,
		Messages: []Message{
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "list files"}}},
			{Role: "assistant", Content: []ContentPart{
				{Type: "tool_call", ToolCallID: "t1", ToolName: "glob", ArgumentsJSON: `{"pattern":"*.go"}`},
			}},
			{Role: "tool", Content: []ContentPart{
				{Type: "tool_result", ToolCallID: "t1", Text: "main.go"},
			}},
		},
	}

	msgs := messagesOf(t, p.buildRequest(req))

	for i, m := range msgs {
		if m["role"] == "tool" {
			t.Fatalf("message[%d] has role \"tool\"; Anthropic only allows user/assistant", i)
		}
	}
	// Last message: user with one tool_result block.
	last := msgs[len(msgs)-1]
	if last["role"] != "user" {
		t.Fatalf("tool result message role = %v, want user", last["role"])
	}
	blocks, ok := last["content"].([]map[string]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("want 1 content block, got %#v", last["content"])
	}
	if blocks[0]["type"] != "tool_result" || blocks[0]["tool_use_id"] != "t1" || blocks[0]["content"] != "main.go" {
		t.Fatalf("bad tool_result block: %#v", blocks[0])
	}
}

func TestBuildRequest_ConsecutiveToolResultsCoalesce(t *testing.T) {
	p := NewAnthropicProvider("k")
	maxTok := 1
	req := &CompletionRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: &maxTok,
		Messages: []Message{
			{Role: "assistant", Content: []ContentPart{
				{Type: "tool_call", ToolCallID: "a", ToolName: "glob", ArgumentsJSON: `{}`},
				{Type: "tool_call", ToolCallID: "b", ToolName: "grep", ArgumentsJSON: `{}`},
			}},
			// Gateway emits one role:"tool" message per result.
			{Role: "tool", Content: []ContentPart{{Type: "tool_result", ToolCallID: "a", Text: "ra"}}},
			{Role: "tool", Content: []ContentPart{{Type: "tool_result", ToolCallID: "b", Text: "rb", IsError: true}}},
		},
	}

	msgs := messagesOf(t, p.buildRequest(req))

	// assistant turn + single coalesced user turn = 2 messages.
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages (assistant + coalesced user), got %d: %#v", len(msgs), msgs)
	}
	user := msgs[1]
	if user["role"] != "user" {
		t.Fatalf("coalesced role = %v, want user", user["role"])
	}
	blocks := user["content"].([]map[string]any)
	if len(blocks) != 2 {
		t.Fatalf("want 2 grouped tool_result blocks, got %d", len(blocks))
	}
	if blocks[0]["tool_use_id"] != "a" || blocks[1]["tool_use_id"] != "b" {
		t.Fatalf("tool_use_id order wrong: %v, %v", blocks[0]["tool_use_id"], blocks[1]["tool_use_id"])
	}
	if blocks[1]["is_error"] != true {
		t.Fatalf("is_error not propagated: %#v", blocks[1])
	}
}

func TestBuildRequest_ToolResultThenUserText(t *testing.T) {
	// A new user turn after tool results must flush the pending tool results
	// first, preserving order: tool-result user message, then the new user text.
	p := NewAnthropicProvider("k")
	maxTok := 1
	req := &CompletionRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: &maxTok,
		Messages: []Message{
			{Role: "tool", Content: []ContentPart{{Type: "tool_result", ToolCallID: "t1", Text: "done"}}},
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "thanks, now continue"}}},
		},
	}

	msgs := messagesOf(t, p.buildRequest(req))
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	if msgs[0]["role"] != "user" {
		t.Fatalf("first (tool result) role = %v, want user", msgs[0]["role"])
	}
	if _, isToolResultArr := msgs[0]["content"].([]map[string]any); !isToolResultArr {
		t.Fatalf("first message content not a block array: %#v", msgs[0]["content"])
	}
	// Second is the plain text user message (single-text fast path → string content).
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "thanks, now continue" {
		t.Fatalf("second message wrong: %#v", msgs[1])
	}
}

func TestBuildRequest_TrailingToolResultFlushed(t *testing.T) {
	// The common real case: conversation ends with a tool result awaiting the
	// model's next turn. Must still be emitted (flushed after the loop).
	p := NewAnthropicProvider("k")
	maxTok := 1
	req := &CompletionRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: &maxTok,
		Messages: []Message{
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "go"}}},
			{Role: "assistant", Content: []ContentPart{{Type: "tool_call", ToolCallID: "t1", ToolName: "glob", ArgumentsJSON: `{}`}}},
			{Role: "tool", Content: []ContentPart{{Type: "tool_result", ToolCallID: "t1", Text: "ok"}}},
		},
	}
	msgs := messagesOf(t, p.buildRequest(req))
	last := msgs[len(msgs)-1]
	if last["role"] != "user" {
		t.Fatalf("trailing tool result not flushed as user message: %#v", last)
	}
}
