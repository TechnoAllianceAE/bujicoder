package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sseServer returns a test server that streams the given raw SSE body.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
}

func collectEvents(t *testing.T, body string) []StreamEvent {
	t.Helper()
	srv := sseServer(t, body)
	defer srv.Close()
	p := newOpenAICompatProvider(OpenAICompatConfig{APIURL: srv.URL, ProviderName: "z-ai"})
	ch, err := p.streamCompletion(context.Background(), &CompletionRequest{Model: "glm-5.1"})
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

// TestEmptyStreamEmitsError reproduces the Z.AI GLM-5.1 failure: a 200 stream
// with usage + finish but no content/reasoning/tool_calls must surface an
// empty_response error (so the gateway fails over) rather than a hollow Complete.
func TestEmptyStreamEmitsError(t *testing.T) {
	cases := map[string]string{
		"no finish reason": "data: {\"choices\":[{\"delta\":{}}]}\n\n" +
			"data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":0}}\n\n" +
			"data: [DONE]\n\n",
		"finish with empty delta": "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5}}\n\n" +
			"data: [DONE]\n\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			events := collectEvents(t, body)
			var sawErr, sawComplete bool
			for _, ev := range events {
				if ev.Error != nil {
					sawErr = true
					if ev.Error.Code != "empty_response" {
						t.Errorf("error code = %q, want empty_response", ev.Error.Code)
					}
				}
				if ev.Complete != nil {
					sawComplete = true
				}
			}
			if !sawErr {
				t.Errorf("expected empty_response error, got events: %+v", events)
			}
			if sawComplete {
				t.Errorf("did not expect Complete on empty stream, got events: %+v", events)
			}
		})
	}
}

// TestContentStreamEmitsComplete guards against false positives: a stream with
// real content must still finalize with a Complete and no empty_response error.
func TestContentStreamEmitsComplete(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n" +
		"data: [DONE]\n\n"
	events := collectEvents(t, body)
	var sawComplete, sawText, sawErr bool
	for _, ev := range events {
		if ev.Complete != nil {
			sawComplete = true
		}
		if ev.Delta != nil && ev.Delta.Text == "hello" {
			sawText = true
		}
		if ev.Error != nil {
			sawErr = true
		}
	}
	if !sawText || !sawComplete || sawErr {
		t.Errorf("want text+complete, no error; got events: %+v", events)
	}
}

// TestToolCallStreamNotEmpty ensures a tool-call-only response (no text) is
// treated as real output, not an empty stream.
func TestToolCallStreamNotEmpty(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read_files\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	events := collectEvents(t, body)
	var sawTool, sawComplete, sawErr bool
	for _, ev := range events {
		if ev.ToolCall != nil {
			sawTool = true
		}
		if ev.Complete != nil {
			sawComplete = true
		}
		if ev.Error != nil {
			sawErr = true
		}
	}
	if !sawTool || !sawComplete || sawErr {
		t.Errorf("want toolcall+complete, no error; got events: %+v", events)
	}
}
