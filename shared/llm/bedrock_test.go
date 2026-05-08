package llm

import (
	"testing"
)

func TestBedrockBuildRequest_MergesConsecutiveToolResults(t *testing.T) {
	p := NewBedrockProvider("k", "us-east-1")
	req := &CompletionRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []ContentPart{
				{Type: "tool_call", ToolCallID: "id_a", ToolName: "f", ArgumentsJSON: `{}`},
				{Type: "tool_call", ToolCallID: "id_b", ToolName: "g", ArgumentsJSON: `{}`},
			}},
			{Role: "tool", Content: []ContentPart{{Type: "tool_result", ToolCallID: "id_a", Text: "ra"}}},
			{Role: "tool", Content: []ContentPart{{Type: "tool_result", ToolCallID: "id_b", Text: "rb"}}},
		},
	}
	body := p.buildRequest(req)
	msgs, ok := body["messages"].([]map[string]any)
	if !ok {
		t.Fatalf("messages not []map[string]any: %T", body["messages"])
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, user-merged-results), got %d: %+v", len(msgs), msgs)
	}
	if msgs[2]["role"] != "user" {
		t.Errorf("expected msg[2] role=user, got %v", msgs[2]["role"])
	}
	content := msgs[2]["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 toolResult blocks merged, got %d: %+v", len(content), content)
	}
	for i, want := range []string{"id_a", "id_b"} {
		tr, ok := content[i]["toolResult"].(map[string]any)
		if !ok {
			t.Fatalf("content[%d] missing toolResult: %+v", i, content[i])
		}
		if got := tr["toolUseId"]; got != want {
			t.Errorf("content[%d] toolUseId = %v, want %v", i, got, want)
		}
	}
}

func TestResolveBedrockModelID(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		region string
		want   string
	}{
		{"anthropic us-east-1", "anthropic.claude-sonnet-4-5-20250929-v1:0", "us-east-1", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"anthropic us-west-2", "anthropic.claude-opus-4-5-20251101-v1:0", "us-west-2", "us.anthropic.claude-opus-4-5-20251101-v1:0"},
		{"anthropic eu-central-1", "anthropic.claude-3-5-sonnet-20241022-v2:0", "eu-central-1", "eu.anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"anthropic ap-southeast-2", "anthropic.claude-3-5-haiku-20241022-v1:0", "ap-southeast-2", "apac.anthropic.claude-3-5-haiku-20241022-v1:0"},
		{"already prefixed us", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us-east-1", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"already prefixed eu", "eu.anthropic.claude-3-5-sonnet-20241022-v2:0", "us-east-1", "eu.anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"nova passthrough", "amazon.nova-pro-v1:0", "us-east-1", "amazon.nova-pro-v1:0"},
		{"ai21 passthrough", "ai21.jamba-1-5-large-v1:0", "us-east-1", "ai21.jamba-1-5-large-v1:0"},
		{"unknown region", "anthropic.claude-3-haiku-20240307-v1:0", "ca-central-1", "anthropic.claude-3-haiku-20240307-v1:0"},
		{"gov region", "anthropic.claude-3-haiku-20240307-v1:0", "us-gov-west-1", "us-gov.anthropic.claude-3-haiku-20240307-v1:0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBedrockModelID(tc.model, tc.region)
			if got != tc.want {
				t.Errorf("resolveBedrockModelID(%q, %q) = %q, want %q", tc.model, tc.region, got, tc.want)
			}
		})
	}
}
