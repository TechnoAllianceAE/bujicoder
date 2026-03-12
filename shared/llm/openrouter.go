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
)

const openRouterAPIURL = "https://openrouter.ai/api/v1/chat/completions"

// OpenRouterProvider implements the Provider interface for OpenRouter's API.
type OpenRouterProvider struct {
	apiKey string
	client *http.Client
}

// NewOpenRouterProvider creates a new OpenRouter provider.
func NewOpenRouterProvider(apiKey string) *OpenRouterProvider {
	return &OpenRouterProvider{
		apiKey: apiKey,
		client: &http.Client{},
	}
}

// Name returns "openrouter".
func (o *OpenRouterProvider) Name() string { return "openrouter" }

// StreamCompletion sends a streaming request to the OpenRouter API (OpenAI-compatible).
func (o *OpenRouterProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	body := o.buildRequest(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal openrouter request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create openrouter request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	httpReq.Header.Set("HTTP-Referer", "https://bujicoder.com")
	httpReq.Header.Set("X-Title", "BujiCoder")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("openrouter API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamEvent, 64)
	go o.processStream(resp.Body, ch)
	return ch, nil
}

func (o *OpenRouterProvider) buildRequest(req *CompletionRequest) map[string]any {
	body := map[string]any{
		"model":          req.Model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}

	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	var messages []map[string]any

	if req.SystemPrompt != nil {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": *req.SystemPrompt,
		})
	}

	for _, m := range req.Messages {
		// Tool result messages: each tool_result part becomes a separate
		// {role:"tool", tool_call_id:"...", content:"..."} message.
		if m.Role == "tool" {
			for _, part := range m.Content {
				if part.Type == "tool_result" {
					messages = append(messages, map[string]any{
						"role":         "tool",
						"tool_call_id": part.ToolCallID,
						"content":      part.Text,
					})
				}
			}
			continue
		}

		msg := map[string]any{"role": m.Role}

		// Collect text, image, and tool_call parts.
		var textBuf strings.Builder
		var toolCalls []map[string]any
		var hasImages bool
		var contentParts []map[string]any
		for _, part := range m.Content {
			switch part.Type {
			case "text":
				textBuf.WriteString(part.Text)
			case "image_url":
				hasImages = true
				if part.ImageURL != nil {
					contentParts = append(contentParts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": part.ImageURL.URL},
					})
				}
			case "tool_call":
				toolCalls = append(toolCalls, map[string]any{
					"id":   part.ToolCallID,
					"type": "function",
					"function": map[string]any{
						"name":      part.ToolName,
						"arguments": part.ArgumentsJSON,
					},
				})
			}
		}

		if hasImages {
			// When images are present, content must be an array of blocks.
			if textBuf.Len() > 0 {
				contentParts = append([]map[string]any{{"type": "text", "text": textBuf.String()}}, contentParts...)
			}
			msg["content"] = contentParts
		} else if textBuf.Len() > 0 {
			msg["content"] = textBuf.String()
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		messages = append(messages, msg)
	}
	body["messages"] = messages

	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
		body["tools"] = tools
	}

	return body
}

func (o *OpenRouterProvider) processStream(body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	var usage UsageInfo
	usage.Provider = "openrouter"

	type pendingToolCall struct {
		id   string
		name string
		args strings.Builder
	}
	pendingTools := make(map[int]*pendingToolCall)

	var completeEmitted bool
	var lastFinishReason string

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) <= 6 || line[:6] != "data: " {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			// If we got usage from a trailing chunk after finish_reason, emit Complete now.
			if !completeEmitted && lastFinishReason != "" {
				ch <- StreamEvent{Complete: &CompleteEvent{FinishReason: lastFinishReason, Usage: usage}}
			}
			return
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Extract usage from any chunk (OpenAI sends it in a separate trailing chunk with empty choices).
		if usageMap, ok := chunk["usage"].(map[string]any); ok {
			if v, ok := usageMap["prompt_tokens"].(float64); ok {
				usage.InputTokens = int(v)
			}
			if v, ok := usageMap["completion_tokens"].(float64); ok {
				usage.OutputTokens = int(v)
			}
		}
		if model, ok := chunk["model"].(string); ok {
			usage.Model = model
		}

		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}

		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		finishReason, _ := choice["finish_reason"].(string)

		if content, ok := delta["content"].(string); ok && content != "" {
			ch <- StreamEvent{Delta: &DeltaEvent{Text: content}}
		}

		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range toolCalls {
				tcMap, _ := tc.(map[string]any)
				idx := int(0)
				if v, ok := tcMap["index"].(float64); ok {
					idx = int(v)
				}

				pt := pendingTools[idx]
				if pt == nil {
					pt = &pendingToolCall{}
					pendingTools[idx] = pt
				}

				if id, ok := tcMap["id"].(string); ok && id != "" {
					pt.id = id
				}
				fn, _ := tcMap["function"].(map[string]any)
				if name, ok := fn["name"].(string); ok && name != "" {
					pt.name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					pt.args.WriteString(args)
				}
			}
		}

		if finishReason != "" {
			// Find the max index to iterate all possible tool calls.
			maxIdx := -1
			for idx := range pendingTools {
				if idx > maxIdx {
					maxIdx = idx
				}
			}
			for idx := 0; idx <= maxIdx; idx++ {
				pt := pendingTools[idx]
				if pt == nil || pt.id == "" || pt.name == "" {
					continue
				}
				args := pt.args.String()
				if args == "" {
					args = "{}"
				}
				// Validate that args is valid JSON before emitting.
				if !json.Valid([]byte(args)) {
					continue
				}
				ch <- StreamEvent{ToolCall: &ToolCallEvent{
					ID:            pt.id,
					Name:          pt.name,
					ArgumentsJSON: args,
				}}
			}
			pendingTools = make(map[int]*pendingToolCall)

			fr := "stop"
			if finishReason == "tool_calls" {
				fr = "tool_calls"
			} else if finishReason == "length" {
				fr = "max_tokens"
			}

			// If we already have usage (same chunk), emit Complete now.
			// Otherwise defer until we get the trailing usage chunk or [DONE].
			if usage.InputTokens > 0 || usage.OutputTokens > 0 {
				ch <- StreamEvent{Complete: &CompleteEvent{FinishReason: fr, Usage: usage}}
				completeEmitted = true
			} else {
				lastFinishReason = fr
			}
		}
	}

	// If stream ended without [DONE] and we haven't emitted Complete, do it now.
	if !completeEmitted && lastFinishReason != "" {
		ch <- StreamEvent{Complete: &CompleteEvent{FinishReason: lastFinishReason, Usage: usage}}
	}
}
