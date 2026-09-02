package llm

import "testing"

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
