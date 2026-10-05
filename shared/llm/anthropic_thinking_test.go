package llm

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestAnthropicStream_PreservesThinkingDeltas(t *testing.T) {
	p := NewAnthropicProviderWithEndpoint("deepseek", "https://api.deepseek.com/anthropic", "test-key")
	sse := `data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Get the value first."}}

data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"The value is 42."}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}

`
	ch := make(chan StreamEvent, 3)
	p.processStream(context.Background(), io.NopCloser(strings.NewReader(sse)), ch)
	thought := <-ch
	if thought.Delta == nil || !thought.Delta.IsReasoning || thought.Delta.Text != "Get the value first." {
		t.Fatalf("thinking delta lost: %#v", thought)
	}
	text := <-ch
	if text.Delta == nil || text.Delta.IsReasoning || text.Delta.Text != "The value is 42." {
		t.Fatalf("text delta changed: %#v", text)
	}
	if ev := <-ch; ev.Complete == nil {
		t.Fatalf("completion lost: %#v", ev)
	}
}

func TestBuildRequest_DeepSeekIncompleteThinkingHistory(t *testing.T) {
	tool := ContentPart{Type: "tool_call", ToolCallID: "t1", ToolName: "get_value", ArgumentsJSON: `{}`}
	thought := ContentPart{Type: "reasoning", Reasoning: "Get the value first."}
	for _, tc := range []struct {
		name     string
		endpoint string
		history  []Message
		tools    bool
		disabled bool
	}{
		{"tool history missing thinking", "https://api.deepseek.com/anthropic", []Message{{Role: "assistant", Content: []ContentPart{tool}}}, true, true},
		{"plain assistant history missing thinking", "https://api.deepseek.com/anthropic", []Message{{Role: "assistant", Content: []ContentPart{{Type: "text", Text: "Hello"}}}}, true, true},
		{"complete thinking history", "https://api.deepseek.com/anthropic", []Message{{Role: "assistant", Content: []ContentPart{thought, tool}}}, true, false},
		{"mixed history", "https://api.deepseek.com/anthropic", []Message{{Role: "assistant", Content: []ContentPart{thought, tool}}, {Role: "assistant", Content: []ContentPart{{Type: "text", Text: "Hello"}}}}, true, true},
		{"no tools", "https://api.deepseek.com/anthropic", []Message{{Role: "assistant", Content: []ContentPart{tool}}}, false, false},
		{"first turn", "https://api.deepseek.com/anthropic", []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "Hello"}}}}, true, false},
		{"native Anthropic", "https://api.anthropic.com", []Message{{Role: "assistant", Content: []ContentPart{tool}}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewAnthropicProviderWithEndpoint("custom", tc.endpoint, "test-key")
			req := &CompletionRequest{Model: "deepseek-v4-pro", Messages: tc.history}
			if tc.tools {
				req.Tools = []ToolDefinition{{Name: "get_value", InputSchema: map[string]any{"type": "object"}}}
			}
			body := p.buildRequest(req)
			thinking, present := body["thinking"]
			if present != tc.disabled {
				t.Fatalf("thinking = %#v, want disabled=%v", thinking, tc.disabled)
			}
			if present && thinking.(map[string]any)["type"] != "disabled" {
				t.Fatalf("thinking = %#v, want disabled", thinking)
			}
			if len(messagesOf(t, body)) != len(tc.history) {
				t.Fatal("conversation history was lost")
			}
		})
	}
}

// Thinking-mode upstreams reject requests whose assistant history dropped the
// thinking blocks the model produced (DeepSeek /anthropic: "The
// `content[].thinking` in the thinking mode must be passed back to the API";
// real Anthropic requires the same for tool-use continuations). buildRequest
// must round-trip reasoning parts as thinking / redacted_thinking blocks.
func TestBuildRequest_ThinkingBlocksRoundTrip(t *testing.T) {
	p := NewAnthropicProvider("k")
	maxTok := 1
	req := &CompletionRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: &maxTok,
		Messages: []Message{
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "go"}}},
			{Role: "assistant", Content: []ContentPart{
				{Type: "reasoning", Reasoning: "step one", Signature: "sig-abc"},
				{Type: "reasoning", Reasoning: "unsigned thought"},
				{Type: "reasoning", Signature: "redacted-blob"},
				{Type: "text", Text: "answer"},
			}},
		},
	}

	msgs := messagesOf(t, p.buildRequest(req))
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	assistant := msgs[1]
	if assistant["role"] != "assistant" {
		t.Fatalf("msgs[1].role = %v, want assistant", assistant["role"])
	}
	blocks, ok := assistant["content"].([]map[string]any)
	if !ok {
		t.Fatalf("assistant content not []map[string]any, got %T", assistant["content"])
	}
	if len(blocks) != 4 {
		t.Fatalf("assistant blocks = %d, want 4 (thinking, thinking, redacted, text)", len(blocks))
	}

	if blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "step one" || blocks[0]["signature"] != "sig-abc" {
		t.Fatalf("blocks[0] = %v, want signed thinking block", blocks[0])
	}
	if blocks[1]["type"] != "thinking" || blocks[1]["thinking"] != "unsigned thought" {
		t.Fatalf("blocks[1] = %v, want unsigned thinking block", blocks[1])
	}
	if _, hasSig := blocks[1]["signature"]; hasSig {
		t.Fatalf("blocks[1] must not carry an empty signature")
	}
	if blocks[2]["type"] != "redacted_thinking" || blocks[2]["data"] != "redacted-blob" {
		t.Fatalf("blocks[2] = %v, want redacted_thinking with data", blocks[2])
	}
	if blocks[3]["type"] != "text" || blocks[3]["text"] != "answer" {
		t.Fatalf("blocks[3] = %v, want text block", blocks[3])
	}
}
