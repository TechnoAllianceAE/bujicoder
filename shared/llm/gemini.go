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

const geminiAPIURL = "https://generativelanguage.googleapis.com/v1beta/models"

// GeminiProvider implements the Provider interface for Google's Gemini API.
type GeminiProvider struct {
	apiKey string
	// baseURL is the models collection URL (default Google's). Override for
	// gateways that re-host the Gemini wire format (e.g. OpenCode Zen's
	// {base}/v1/models collection).
	baseURL string
	name    string
	// bearer, when set, is also sent as Authorization (some gateways front
	// the native API with bearer auth in addition to x-goog-api-key).
	bearer string
	// ExtraHeaders are sent on every request; RequestHeaders, when set, adds
	// per-request headers and wins on conflict.
	ExtraHeaders   map[string]string
	RequestHeaders func(req *CompletionRequest) map[string]string
	client         *http.Client
}

// NewGeminiProvider creates a new Gemini provider.
// The optional timeout bounds the connect/headers phase, NOT the streaming
// body — long completions must not be killed by a client deadline. Total
// stream duration is bounded by the request context.
func NewGeminiProvider(apiKey string, timeout ...time.Duration) *GeminiProvider {
	headerTimeout := 90 * time.Second
	if len(timeout) > 0 && timeout[0] > 0 {
		headerTimeout = timeout[0]
	}
	return &GeminiProvider{
		apiKey:  apiKey,
		baseURL: geminiAPIURL,
		name:    "google",
		client: &http.Client{
			Transport: sharedPooledTransport(headerTimeout),
		},
	}
}

// NewGeminiProviderWithEndpoint creates a Gemini-native provider pointed at a
// custom models collection URL under a custom registry name. Used for
// gateways that re-host the Gemini wire format (e.g. OpenCode Zen). bearer,
// when non-empty, is additionally sent as Authorization.
func NewGeminiProviderWithEndpoint(name, baseURL, apiKey, bearer string, timeout ...time.Duration) *GeminiProvider {
	p := NewGeminiProvider(apiKey, timeout...)
	if name != "" {
		p.name = name
	}
	if b := strings.TrimRight(strings.TrimSpace(baseURL), "/"); b != "" {
		p.baseURL = b
	}
	p.bearer = bearer
	return p
}

// Name returns the provider's registry name ("google" by default).
func (g *GeminiProvider) Name() string { return g.name }

// APIKey returns the provider's API key.
func (g *GeminiProvider) APIKey() string { return g.apiKey }

// StreamCompletion sends a streaming request to the Gemini API.
func (g *GeminiProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	body := g.buildRequest(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	// The API key goes in the x-goog-api-key header, never the query string:
	// net/http wraps transport failures in *url.Error, whose Error() prints the
	// full URL, so a key in the query leaks into every log line and TUI error
	// message on any network failure.
	url := fmt.Sprintf("%s/%s:streamGenerateContent?alt=sse", g.baseURL, req.Model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create gemini request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.apiKey)
	if g.bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+g.bearer)
	}
	for k, v := range g.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	if g.RequestHeaders != nil {
		for k, v := range g.RequestHeaders(req) {
			httpReq.Header.Set(k, v)
		}
	}

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody := readErrorBody(resp)
		// Parse Retry-After header for rate limit responses
		headers := NormalizeHeaders(resp.Header)
		retryAfter := ExtractRetryAfterFromHeaders(headers)
		return nil, NewProviderError(resp.StatusCode, respBody, retryAfter)
	}

	ch := make(chan StreamEvent, 64)
	go g.processStream(ctx, resp.Body, ch)
	return ch, nil
}

func (g *GeminiProvider) buildRequest(req *CompletionRequest) map[string]any {
	body := map[string]any{}

	// System instruction
	if req.SystemPrompt != nil {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{
				{"text": *req.SystemPrompt},
			},
		}
	}

	// Generation config
	genConfig := map[string]any{}
	if req.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		genConfig["temperature"] = *req.Temperature
	}
	if len(genConfig) > 0 {
		body["generationConfig"] = genConfig
	}

	// Convert messages to Gemini's contents format
	var contents []map[string]any
	for _, m := range req.Messages {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}

		var parts []map[string]any
		for _, part := range m.Content {
			switch part.Type {
			case "text":
				parts = append(parts, map[string]any{"text": part.Text})
			case "tool_call":
				var args any
				_ = json.Unmarshal([]byte(part.ArgumentsJSON), &args)
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": part.ToolName,
						"args": args,
					},
				})
			case "tool_result":
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name": part.ToolName,
						"response": map[string]any{
							"content": part.Text,
						},
					},
				})
			}
		}
		if len(parts) > 0 {
			contents = append(contents, map[string]any{
				"role":  role,
				"parts": parts,
			})
		}
	}
	body["contents"] = contents

	// Tools (function declarations)
	if len(req.Tools) > 0 {
		var funcDecls []map[string]any
		for _, t := range req.Tools {
			funcDecls = append(funcDecls, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			})
		}
		body["tools"] = []map[string]any{
			{"functionDeclarations": funcDecls},
		}
	}

	return body
}

func (g *GeminiProvider) processStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var usage UsageInfo
	usage.Provider = g.name

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) <= 6 || line[:6] != "data: " {
			continue
		}
		data := line[6:]

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		candidates, _ := chunk["candidates"].([]any)
		if len(candidates) == 0 {
			// Check for usage metadata without candidates
			if um, ok := chunk["usageMetadata"].(map[string]any); ok {
				if v, ok := um["promptTokenCount"].(float64); ok {
					usage.InputTokens = int(v)
				}
				if v, ok := um["candidatesTokenCount"].(float64); ok {
					usage.OutputTokens = int(v)
				}
			}
			continue
		}

		candidate, _ := candidates[0].(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		finishReason, _ := candidate["finishReason"].(string)

		for _, p := range parts {
			part, _ := p.(map[string]any)

			// Text content
			if text, ok := part["text"].(string); ok && text != "" {
				if !sendEvent(ctx, ch, StreamEvent{Delta: &DeltaEvent{Text: text}}) {
					return
				}
			}

			// Function call
			if fc, ok := part["functionCall"].(map[string]any); ok {
				name, _ := fc["name"].(string)
				args, _ := fc["args"].(map[string]any)
				argsJSON, _ := json.Marshal(args)
				if !sendEvent(ctx, ch, StreamEvent{ToolCall: &ToolCallEvent{
					ID:            fmt.Sprintf("call_%s", name),
					Name:          name,
					ArgumentsJSON: string(argsJSON),
				}}) {
					return
				}
			}
		}

		// Extract usage metadata
		if um, ok := chunk["usageMetadata"].(map[string]any); ok {
			if v, ok := um["promptTokenCount"].(float64); ok {
				usage.InputTokens = int(v)
			}
			if v, ok := um["candidatesTokenCount"].(float64); ok {
				usage.OutputTokens = int(v)
			}
		}

		if model, ok := chunk["modelVersion"].(string); ok {
			usage.Model = model
		}

		if finishReason != "" {
			fr := "stop"
			switch finishReason {
			case "MAX_TOKENS":
				fr = "max_tokens"
			case "TOOL_CALLS", "FUNCTION_CALL":
				fr = "tool_calls"
			}
			if !sendEvent(ctx, ch, StreamEvent{Complete: &CompleteEvent{FinishReason: fr, Usage: usage}}) {
				return
			}
		}
	}

	// Distinguish clean EOF from a network/parse error so the gateway can
	// surface truncations instead of treating them as successful streams.
	if err := scanner.Err(); err != nil {
		sendEvent(ctx, ch, StreamEvent{Error: &ErrorEvent{
			Code:    "stream_truncated",
			Message: fmt.Sprintf("gemini stream read error: %v", err),
		}})
	}
}
