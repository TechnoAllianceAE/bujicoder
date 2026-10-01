package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesModelRouting(t *testing.T) {
	for _, id := range []string{
		"gpt-5.6-luna", "gpt-5.3-codex", "gpt-5-nano",
		"grok-4.5", "grok-build-0.1",
		"muse-spark-1.3", "muse-spark-1.3-contributor-free",
	} {
		if !isResponsesModel(id) {
			t.Errorf("isResponsesModel(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"kimi-k2.6", "glm-5.3-flash", "deepseek-v4-flash", "big-pickle",
		"qwen3.8-max", "minimax-m3", "mimo-v2.5-free",
		"claude-sonnet-4-5", "gemini-3-flash",
	} {
		if isResponsesModel(id) {
			t.Errorf("isResponsesModel(%q) = true, want false", id)
		}
	}
	if !needsAnthropicMessages("claude-sonnet-4-5") || !needsAnthropicMessages("qwen3.7-max") {
		t.Errorf("needsAnthropicMessages missed claude/qwen ids")
	}
	if needsAnthropicMessages("qwen3.8-max") {
		t.Errorf("qwen3.8-max is chat/completions, not messages")
	}
	if !isGeminiModel("gemini-3-flash") || isGeminiModel("gpt-5.6-luna") {
		t.Errorf("isGeminiModel wrong")
	}
}

// TestResponsesStreamParsesTextToolCallsAndUsage feeds a realistic Responses
// SSE stream (reasoning + text + one function call + usage) and checks the
// emitted event sequence.
func TestResponsesStreamParsesTextToolCallsAndUsage(t *testing.T) {
	var gotUA, gotClient, gotSession, gotReqID, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotClient, gotSession, gotReqID, gotPath =
			r.Header.Get("User-Agent"), r.Header.Get("x-opencode-client"),
			r.Header.Get("x-opencode-session"), r.Header.Get("x-opencode-request-id"),
			r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.reasoning_summary_text.delta\n" +
				"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
				"event: response.output_item.added\n" +
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"Bash\",\"arguments\":\"\"}}\n\n" +
				"event: response.function_call_arguments.delta\n" +
				"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.6-luna\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"Bash\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}],\"usage\":{\"input_tokens\":19,\"output_tokens\":61}}}\n\n",
		))
	}))
	defer srv.Close()

	p := newOpenCode("opencode", "http://127.0.0.1:1", srv.URL, "k")
	ch, err := p.StreamCompletion(context.Background(), &CompletionRequest{
		RequestID: "req-x", UserID: "u1", Model: "gpt-5.6-luna",
		Messages: []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var texts, reasoning []string
	var tools []ToolCallEvent
	var complete *CompleteEvent
	var streamErr *ErrorEvent
	for ev := range ch {
		switch {
		case ev.Delta != nil && ev.Delta.IsReasoning:
			reasoning = append(reasoning, ev.Delta.Text)
		case ev.Delta != nil:
			texts = append(texts, ev.Delta.Text)
		case ev.ToolCall != nil:
			tools = append(tools, *ev.ToolCall)
		case ev.Complete != nil:
			complete = ev.Complete
		case ev.Error != nil:
			streamErr = ev.Error
		}
	}
	if streamErr != nil {
		t.Fatalf("stream error: %+v", streamErr)
	}
	if strings.Join(reasoning, "") != "thinking" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if strings.Join(texts, "") != "hi" {
		t.Errorf("texts = %q", texts)
	}
	if len(tools) != 1 || tools[0].ID != "call_1" || tools[0].Name != "Bash" || tools[0].ArgumentsJSON != `{"cmd":"ls"}` {
		t.Errorf("tools = %+v", tools)
	}
	if complete == nil {
		t.Fatalf("no Complete event")
	}
	if complete.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls", complete.FinishReason)
	}
	if complete.Usage.InputTokens != 19 || complete.Usage.OutputTokens != 61 || complete.Usage.Model != "gpt-5.6-luna" {
		t.Errorf("usage = %+v", complete.Usage)
	}
	if gotPath != "/" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotUA, "opencode/") || gotClient != "cli" ||
		!strings.HasPrefix(gotSession, "ses_") || !strings.HasPrefix(gotReqID, "msg_") {
		t.Errorf("headers ua=%q client=%q session=%q reqid=%q", gotUA, gotClient, gotSession, gotReqID)
	}
}

// TestResponsesUnsupportedFamiliesFailFast ensures claude/gemini ids get a
// clear client-side error instead of an upstream 400.
func TestResponsesUnsupportedFamiliesFailFast(t *testing.T) {
	p := NewOpenCodeProvider("k")
	for _, id := range []string{"claude-sonnet-4-5", "gemini-3-flash"} {
		ch, err := p.StreamCompletion(context.Background(), &CompletionRequest{Model: id})
		if err == nil {
			for range ch {
			}
			t.Errorf("model %q: expected error, got stream", id)
			continue
		}
		if !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("model %q: error = %q, want protocol guidance", id, err)
		}
	}
}

// TestResponsesBuildRequestShape checks the Responses request body: system
// prompt first, input_text blocks, function tools + tool_choice, and
// max_output_tokens (not max_tokens).
func TestResponsesBuildRequestShape(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	maxTok := 150
	p := newOpenCode("opencode", "http://127.0.0.1:1", srv.URL, "k")
	sys := "be nice"
	ch, err := p.StreamCompletion(context.Background(), &CompletionRequest{
		RequestID: "r", UserID: "u", Model: "gpt-5.6-luna", MaxTokens: &maxTok,
		SystemPrompt: &sys,
		Messages:     []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "hi"}}}},
		Tools:        []ToolDefinition{{Name: "Bash", Description: "run", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if captured["model"] != "gpt-5.6-luna" || captured["stream"] != true {
		t.Errorf("top-level = %v", captured)
	}
	if captured["max_output_tokens"] != float64(150) {
		t.Errorf("max_output_tokens = %v", captured["max_output_tokens"])
	}
	if _, ok := captured["max_tokens"]; ok {
		t.Errorf("chat-style max_tokens must not be sent")
	}
	if captured["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v", captured["tool_choice"])
	}
	input, _ := captured["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input items = %v", captured["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be nice" {
		t.Errorf("system item = %v", first)
	}
}
