package llm

import "testing"

// Custom OpenAI-format providers (e.g. a directly-registered DeepSeek) must
// echo assistant reasoning back as reasoning_content: thinking-mode upstreams
// reject the request without it. The echo is conditional on the history
// actually containing reasoning parts, so non-thinking traffic is unchanged.
func TestCustomOpenAI_EchoesReasoningContent(t *testing.T) {
	p := NewCustomOpenAIProvider("deepseek", "https://api.deepseek.com/v1", "k")
	maxTok := 1
	req := &CompletionRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: &maxTok,
		Messages: []Message{
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "go"}}},
			{Role: "assistant", Content: []ContentPart{
				{Type: "reasoning", Reasoning: "chain of thought"},
				{Type: "text", Text: "answer"},
			}},
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "more"}}},
		},
	}

	msgs := messagesOf(t, p.compat.buildRequest(req))
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	assistant := msgs[1]
	rc, ok := assistant["reasoning_content"].(string)
	if !ok || rc != "chain of thought" {
		t.Fatalf("assistant reasoning_content = %v, want echoed reasoning", assistant["reasoning_content"])
	}
	if assistant["content"] != "answer" {
		t.Fatalf("assistant content = %v, want \"answer\"", assistant["content"])
	}

	// Plain conversation: no reasoning parts anywhere, so the field must not
	// appear at all (strict OpenAI-compatible upstreams reject unknown fields).
	plain := &CompletionRequest{
		Model:     "deepseek-chat",
		MaxTokens: &maxTok,
		Messages: []Message{
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}},
		},
	}
	for i, m := range messagesOf(t, p.compat.buildRequest(plain)) {
		if _, has := m["reasoning_content"]; has {
			t.Fatalf("messages[%d] carries reasoning_content in a non-thinking conversation", i)
		}
	}
}
