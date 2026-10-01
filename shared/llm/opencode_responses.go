package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file implements the OpenAI Responses API client used for the Zen model
// families that don't speak chat/completions. Zen serves three protocols
// (see https://opencode.ai/docs/zen):
//
//   - chat/completions  — kimi, glm, deepseek, minimax, big-pickle, *-free, …
//   - responses         — gpt-*, grok-*, muse-spark-*
//   - messages          — claude-*, some qwen-* (Anthropic-native; no client
//     yet, callers get a clear error instead of an upstream 400)
//
// Asking a responses-family model on chat/completions fails with
// 400 ModelProtocolUnsupported, which is how the gap was found
// (opencode/gpt-5.6-luna via Test Model, 2026-10-01).

// responsesModelPrefixes are matched against the bare model id.
var responsesModelPrefixes = []string{"gpt-", "grok-", "muse-spark-"}

// anthropicMessagesModels need the Anthropic messages protocol, which no
// provider implements yet. Explicit ids (prefix rules can't cover qwen: e.g.
// qwen3.8-max is chat/completions while qwen3.7-max is messages).
var anthropicMessagesModels = map[string]bool{
	"qwen3.7-max": true, "qwen3.7-plus": true,
	"qwen3.6-plus": true, "qwen3.5-plus": true,
	"qwen3.8-flash": true,
}

// isResponsesModel reports whether a bare model id needs the Responses API.
func isResponsesModel(id string) bool {
	for _, p := range responsesModelPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// needsAnthropicMessages reports whether a bare model id needs the Anthropic
// messages protocol (claude-* family or listed qwen ids). Gemini ids are
// Google-native and match neither protocol.
func needsAnthropicMessages(id string) bool {
	return strings.HasPrefix(id, "claude-") || anthropicMessagesModels[id]
}

func isGeminiModel(id string) bool { return strings.HasPrefix(id, "gemini-") }

// OpenCodeResponsesURL returns the Responses-API URL for a tier ("go" or "zen").
func OpenCodeResponsesURL(tier string) string {
	return zenBase(tier) + "/" + OpenCodeAPIVersion() + "/responses"
}

// openAIResponsesProvider streams via the OpenAI Responses API
// (POST {base}/responses, SSE). Wire format follows the public OpenAI spec:
// request {model, input[], stream, tools}; events
// response.output_text.delta / response.reasoning_*_text.delta /
// response.output_item.added (function_call) /
// response.function_call_arguments.delta / response.completed|failed.
type openAIResponsesProvider struct {
	apiURL       string
	apiKey       string
	providerName string
	client       *http.Client
}

func newOpenAIResponsesProvider(apiURL, apiKey, providerName string, timeout time.Duration) *openAIResponsesProvider {
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	return &openAIResponsesProvider{
		apiURL: apiURL, apiKey: apiKey, providerName: providerName,
		client: &http.Client{Transport: sharedPooledTransport(timeout)},
	}
}

func (p *openAIResponsesProvider) streamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return p.doStreamRequest(ctx, req, false)
}

func (p *openAIResponsesProvider) doStreamRequest(ctx context.Context, req *CompletionRequest, stripTools bool) (<-chan StreamEvent, error) {
	body := p.buildRequest(req)
	if stripTools {
		delete(body, "tools")
		delete(body, "tool_choice")
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s responses request: %w", p.providerName, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create %s responses request: %w", p.providerName, err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for k, v := range OpenCodeClientHeaders(opencodeSession(req)) {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s responses request: %w", p.providerName, err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody := readErrorBody(resp)
		if !stripTools && resp.StatusCode == http.StatusBadRequest &&
			strings.Contains(respBody, "does not support tools") {
			return p.doStreamRequest(ctx, req, true)
		}
		headers := NormalizeHeaders(resp.Header)
		retryAfter := ExtractRetryAfterFromHeaders(headers)
		return nil, NewProviderError(resp.StatusCode, respBody, retryAfter)
	}

	ch := make(chan StreamEvent, 64)
	go p.processStream(ctx, resp.Body, ch)
	return ch, nil
}

// buildRequest converts a CompletionRequest to the Responses API shape.
// Roles map to input items; prior assistant tool calls become function_call
// items and tool results become function_call_output items so multi-turn
// agent loops keep working.
func (p *openAIResponsesProvider) buildRequest(req *CompletionRequest) map[string]any {
	body := map[string]any{
		"model":  req.Model,
		"stream": true,
	}

	if req.MaxTokens != nil {
		body["max_output_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	var input []map[string]any

	if req.SystemPrompt != nil {
		input = append(input, map[string]any{
			"role":    "system",
			"content": *req.SystemPrompt,
		})
	}

	for _, m := range req.Messages {
		// Tool results feed back as function_call_output items.
		if m.Role == "tool" {
			for _, part := range m.Content {
				if part.Type == "tool_result" {
					input = append(input, map[string]any{
						"type":    "function_call_output",
						"call_id": part.ToolCallID,
						"output":  part.Text,
					})
				}
			}
			continue
		}

		// Prior assistant tool calls replay as function_call items (plus any
		// text as an output_text block) so the model sees its own calls.
		if m.Role == "assistant" {
			var textBuf strings.Builder
			for _, part := range m.Content {
				switch part.Type {
				case "text":
					textBuf.WriteString(part.Text)
				case "tool_call":
					item := map[string]any{
						"type":      "function_call",
						"call_id":   part.ToolCallID,
						"name":      part.ToolName,
						"arguments": part.ArgumentsJSON,
					}
					input = append(input, item)
				}
			}
			if textBuf.Len() > 0 {
				input = append(input, map[string]any{
					"role":    "assistant",
					"content": []map[string]any{{"type": "output_text", "text": textBuf.String()}},
				})
			}
			continue
		}

		// User/system turns: text + images as input content blocks.
		var blocks []map[string]any
		for _, part := range m.Content {
			switch part.Type {
			case "text":
				if part.Text != "" {
					blocks = append(blocks, map[string]any{"type": "input_text", "text": part.Text})
				}
			case "image_url":
				if part.ImageURL != nil {
					blocks = append(blocks, map[string]any{"type": "input_image", "image_url": part.ImageURL.URL})
				}
			}
		}
		if len(blocks) == 0 {
			blocks = append(blocks, map[string]any{"type": "input_text", "text": ""})
		}
		input = append(input, map[string]any{"role": m.Role, "content": blocks})
	}
	body["input"] = input

	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			})
		}
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}

	return body
}

func (p *openAIResponsesProvider) processStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	var usage UsageInfo
	usage.Provider = p.providerName

	var completeEmitted bool
	// producedOutput tracks usable content (text/reasoning/tool calls); an
	// output-less stream yields empty_response so the gateway can fail over
	// instead of forwarding a hollow message (same policy as chat compat).
	var producedOutput bool
	emitTerminal := func(fr string) {
		if completeEmitted {
			return
		}
		completeEmitted = true
		if producedOutput {
			sendEvent(ctx, ch, StreamEvent{Complete: &CompleteEvent{FinishReason: fr, Usage: usage}})
			return
		}
		sendEvent(ctx, ch, StreamEvent{Error: &ErrorEvent{
			Code:    "empty_response",
			Message: fmt.Sprintf("%s returned an empty response (no content)", p.providerName),
		}})
	}

	// Pending function calls by output_index: response.output_item.added
	// carries call_id+name, response.function_call_arguments.delta streams the
	// JSON args. Flushed on response.completed (or [DONE]).
	type pendingFnCall struct {
		CallID string
		Name   string
		Args   strings.Builder
	}
	pending := make(map[int]*pendingFnCall)
	// flushTools emits one ToolCallEvent per accumulated call and reports
	// whether anything was emitted (drives the tool_calls finish reason).
	flushTools := func() bool {
		emitted := false
		for idx := 0; idx < len(pending); idx++ {
			if pt, ok := pending[idx]; ok {
				id := pt.CallID
				if id == "" {
					id = fmt.Sprintf("call_%d", idx)
				}
				sendEvent(ctx, ch, StreamEvent{ToolCall: &ToolCallEvent{
					ID:            id,
					Name:          pt.Name,
					ArgumentsJSON: pt.Args.String(),
				}})
				producedOutput = true
				emitted = true
			}
		}
		pending = make(map[int]*pendingFnCall)
		return emitted
	}
	pendingFor := func(idx int) *pendingFnCall {
		pt, ok := pending[idx]
		if !ok {
			pt = &pendingFnCall{}
			pending[idx] = pt
		}
		return pt
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue // blank / keepalive
		}
		var evType string
		var data string
		if strings.HasPrefix(line, "event: ") {
			evType = strings.TrimSpace(line[7:])
			// The data line follows; read ahead one line.
			if !scanner.Scan() {
				break
			}
			line = scanner.Text()
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data = line[6:]
		if data == "[DONE]" {
			fr := "stop"
			if flushTools() {
				fr = "tool_calls"
			}
			emitTerminal(fr)
			return
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}
		if t, ok := payload["type"].(string); ok && evType == "" {
			evType = t
		}

		switch evType {
		case "response.output_text.delta":
			if d, ok := payload["delta"].(string); ok && d != "" {
				if !sendEvent(ctx, ch, StreamEvent{Delta: &DeltaEvent{Text: d}}) {
					return
				}
				producedOutput = true
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if d, ok := payload["delta"].(string); ok && d != "" {
				if !sendEvent(ctx, ch, StreamEvent{Delta: &DeltaEvent{Text: d, IsReasoning: true}}) {
					return
				}
				producedOutput = true
			}
		case "response.output_item.added":
			idx := eventIndex(payload)
			if item, ok := payload["item"].(map[string]any); ok {
				if t, _ := item["type"].(string); t == "function_call" {
					pt := pendingFor(idx)
					if id, ok := item["call_id"].(string); ok && id != "" {
						pt.CallID = id
					}
					if name, ok := item["name"].(string); ok && name != "" {
						pt.Name = name
					}
					if args, ok := item["arguments"].(string); ok && args != "" {
						pt.Args.WriteString(args)
					}
				}
			}
		case "response.function_call_arguments.delta":
			idx := eventIndex(payload)
			if d, ok := payload["delta"].(string); ok {
				pendingFor(idx).Args.WriteString(d)
			}
		case "response.completed":
			if respObj, ok := payload["response"].(map[string]any); ok {
				if u, ok := respObj["usage"].(map[string]any); ok {
					if v, ok := u["input_tokens"].(float64); ok {
						usage.InputTokens = int(v)
					}
					if v, ok := u["output_tokens"].(float64); ok {
						usage.OutputTokens = int(v)
					}
				}
				if m, ok := respObj["model"].(string); ok {
					usage.Model = m
				}
				// Belt-and-braces: harvest complete function_call items from
				// the output array in case argument deltas never streamed.
				if out, ok := respObj["output"].([]any); ok {
					for _, it := range out {
						im, _ := it.(map[string]any)
						if im == nil || im["type"] != "function_call" {
							continue
						}
						callID, _ := im["call_id"].(string)
						name, _ := im["name"].(string)
						args, _ := im["arguments"].(string)
						matched := false
						for _, pt := range pending {
							if pt.CallID == callID || (callID == "" && pt.Name == name && pt.Args.Len() == 0) {
								if pt.Args.Len() == 0 {
									pt.Args.WriteString(args)
								}
								matched = true
								break
							}
						}
						if !matched && (callID != "" || name != "") {
							pt := &pendingFnCall{CallID: callID, Name: name}
							pt.Args.WriteString(args)
							pending[len(pending)] = pt
						}
						_ = matched
					}
				}
			}
			fr := "stop"
			if flushTools() {
				fr = "tool_calls"
			}
			emitTerminal(fr)
			return
		case "response.failed", "response.incomplete":
			msg := p.providerName + " response " + evType
			if respObj, ok := payload["response"].(map[string]any); ok {
				if e, ok := respObj["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok && m != "" {
						msg += ": " + m
					}
				}
			}
			sendEvent(ctx, ch, StreamEvent{Error: &ErrorEvent{Code: "provider_error", Message: msg}})
			return
		}
	}

	if err := scanner.Err(); err != nil {
		sendEvent(ctx, ch, StreamEvent{Error: &ErrorEvent{
			Code:    "stream_truncated",
			Message: fmt.Sprintf("%s responses stream read error: %v", p.providerName, err),
		}})
		return
	}
	fr := "stop"
	if flushTools() {
		fr = "tool_calls"
	}
	emitTerminal(fr)
}

func eventIndex(payload map[string]any) int {
	if v, ok := payload["output_index"].(float64); ok {
		return int(v)
	}
	return 0
}
