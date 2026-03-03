package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const geminiAPIURL = "https://generativelanguage.googleapis.com/v1beta/models"

// GeminiProvider implements the Provider interface for Google's Gemini API.
type GeminiProvider struct {
	apiKey string
	client *http.Client
}

// NewGeminiProvider creates a new Gemini provider.
func NewGeminiProvider(apiKey string) *GeminiProvider {
	return &GeminiProvider{
		apiKey: apiKey,
		client: &http.Client{},
	}
}

// Name returns "google".
func (g *GeminiProvider) Name() string { return "google" }

// StreamCompletion sends a streaming request to the Gemini API.
func (g *GeminiProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	body := g.buildRequest(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:streamGenerateContent?alt=sse&key=%s", geminiAPIURL, req.Model, g.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create gemini request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamEvent, 64)
	go g.processStream(resp.Body, ch)
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

func (g *GeminiProvider) processStream(body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var usage UsageInfo
	usage.Provider = "google"

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
				ch <- StreamEvent{Delta: &DeltaEvent{Text: text}}
			}

			// Function call
			if fc, ok := part["functionCall"].(map[string]any); ok {
				name, _ := fc["name"].(string)
				args, _ := fc["args"].(map[string]any)
				argsJSON, _ := json.Marshal(args)
				ch <- StreamEvent{ToolCall: &ToolCallEvent{
					ID:            fmt.Sprintf("call_%s", name),
					Name:          name,
					ArgumentsJSON: string(argsJSON),
				}}
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
			if finishReason == "STOP" {
				fr = "stop"
			} else if finishReason == "MAX_TOKENS" {
				fr = "max_tokens"
			} else if finishReason == "TOOL_CALLS" || finishReason == "FUNCTION_CALL" {
				fr = "tool_calls"
			}
			ch <- StreamEvent{Complete: &CompleteEvent{FinishReason: fr, Usage: usage}}
		}
	}
}
