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

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"

// AnthropicProvider implements the Provider interface for Anthropic's API.
type AnthropicProvider struct {
	apiKey string
	client *http.Client
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

// Name returns "anthropic".
func (a *AnthropicProvider) Name() string { return "anthropic" }

// APIKey returns the provider's API key for direct passthrough proxying.
func (a *AnthropicProvider) APIKey() string { return a.apiKey }

// StreamCompletion sends a streaming request to the Anthropic Messages API.
func (a *AnthropicProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	body := a.buildRequest(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create anthropic request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if req.OAuthToken != "" {
		// OAuth subscription mode: charged against user's Claude Pro/Max/Team plan.
		httpReq.Header.Set("Authorization", "Bearer "+req.OAuthToken)
		httpReq.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	} else {
		httpReq.Header.Set("x-api-key", a.apiKey)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Parse Retry-After header for rate limit responses
		headers := NormalizeHeaders(resp.Header)
		retryAfter := ExtractRetryAfterFromHeaders(headers)
		return nil, NewProviderError(resp.StatusCode, string(body), retryAfter)
	}

	ch := make(chan StreamEvent, 64)
	go a.processStream(resp.Body, ch)
	return ch, nil
}

func (a *AnthropicProvider) buildRequest(req *CompletionRequest) map[string]any {
	body := map[string]any{
		"model":  req.Model,
		"stream": true,
	}

	if req.SystemPrompt != nil {
		body["system"] = *req.SystemPrompt
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	} else {
		body["max_tokens"] = 8192
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	var messages []map[string]any
	for _, m := range req.Messages {
		msg := map[string]any{"role": m.Role}
		if len(m.Content) == 1 && m.Content[0].Type == "text" {
			msg["content"] = m.Content[0].Text
		} else {
			var content []map[string]any
			for _, part := range m.Content {
				switch part.Type {
				case "text":
					content = append(content, map[string]any{"type": "text", "text": part.Text})
				case "image_url":
					if part.ImageURL != nil {
						// Anthropic requires base64 data without the data URI prefix.
						imgData := part.ImageURL.URL
						mediaType := part.ImageURL.MediaType
						if mediaType == "" {
							mediaType = "image/png"
						}
						// Strip data URI prefix if present (e.g., "data:image/png;base64,...")
						if idx := strings.Index(imgData, ","); idx != -1 && strings.HasPrefix(imgData, "data:") {
							imgData = imgData[idx+1:]
						}
						content = append(content, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": mediaType,
								"data":       imgData,
							},
						})
					}
				case "tool_call":
					var args any
					_ = json.Unmarshal([]byte(part.ArgumentsJSON), &args)
					content = append(content, map[string]any{
						"type":  "tool_use",
						"id":    part.ToolCallID,
						"name":  part.ToolName,
						"input": args,
					})
				case "tool_result":
					content = append(content, map[string]any{
						"type":        "tool_result",
						"tool_use_id": part.ToolCallID,
						"content":     part.Text,
						"is_error":    part.IsError,
					})
				}
			}
			msg["content"] = content
		}
		messages = append(messages, msg)
	}
	body["messages"] = messages

	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			})
		}
		body["tools"] = tools
	}

	return body
}

func (a *AnthropicProvider) processStream(body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	var usage UsageInfo
	usage.Provider = "anthropic"

	var pendingToolID, pendingToolName string
	var argsBuffer strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 || line[0] != 'd' {
			continue
		}
		// SSE format: "data: {...}"
		if len(line) <= 6 {
			continue
		}
		data := line[6:]

		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		switch eventType {
		case "content_block_start":
			cb, _ := event["content_block"].(map[string]any)
			cbType, _ := cb["type"].(string)
			if cbType == "tool_use" {
				pendingToolID, _ = cb["id"].(string)
				pendingToolName, _ = cb["name"].(string)
				argsBuffer.Reset()
			}

		case "content_block_delta":
			delta, _ := event["delta"].(map[string]any)
			deltaType, _ := delta["type"].(string)
			switch deltaType {
			case "text_delta":
				text, _ := delta["text"].(string)
				ch <- StreamEvent{Delta: &DeltaEvent{Text: text}}
			case "input_json_delta":
				if partial, ok := delta["partial_json"].(string); ok {
					argsBuffer.WriteString(partial)
				}
			}

		case "content_block_stop":
			if pendingToolID != "" {
				args := argsBuffer.String()
				if args == "" {
					args = "{}"
				}
				ch <- StreamEvent{ToolCall: &ToolCallEvent{
					ID:            pendingToolID,
					Name:          pendingToolName,
					ArgumentsJSON: args,
				}}
				pendingToolID = ""
				pendingToolName = ""
				argsBuffer.Reset()
			}

		case "message_delta":
			delta, _ := event["delta"].(map[string]any)
			stopReason, _ := delta["stop_reason"].(string)
			usageInfo, _ := event["usage"].(map[string]any)
			if outputTokens, ok := usageInfo["output_tokens"].(float64); ok {
				usage.OutputTokens = int(outputTokens)
			}

			finishReason := "stop"
			if stopReason == "tool_use" {
				finishReason = "tool_calls"
			} else if stopReason == "max_tokens" {
				finishReason = "max_tokens"
			}

			ch <- StreamEvent{Complete: &CompleteEvent{
				FinishReason: finishReason,
				Usage:        usage,
			}}

		case "message_start":
			msg, _ := event["message"].(map[string]any)
			usageInfo, _ := msg["usage"].(map[string]any)
			if inputTokens, ok := usageInfo["input_tokens"].(float64); ok {
				usage.InputTokens = int(inputTokens)
			}
			if model, ok := msg["model"].(string); ok {
				usage.Model = model
			}
		}
	}
}
