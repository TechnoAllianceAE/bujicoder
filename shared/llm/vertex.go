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
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// VertexProvider implements the Provider interface for Google Cloud's Vertex AI.
// It relies on Google Application Default Credentials for authentication.
//
// Vertex hosts multiple publishers (Google, Anthropic, Mistral, Meta…) and each
// publisher pins its models to a subset of regions. A single default `region`
// doesn't suit every publisher — Claude in particular is not served from
// us-central1 even though Gemini is. publisherRegions lets the operator map a
// publisher name to a region override; requests fall back to `region` when the
// publisher has no entry.
type VertexProvider struct {
	projectID        string
	region           string
	publisherRegions map[string]string
	client           *http.Client
	catalogMu sync.RWMutex
	catalogCache []ModelInfo
	catalogLastRefreshed time.Time
}

// SetPublisherRegion overrides the default region for a single publisher
// (e.g. "anthropic" → "us-east5"). Empty region clears the override.
func (v *VertexProvider) SetPublisherRegion(publisher, region string) {
	if v == nil {
		return
	}
	if v.publisherRegions == nil {
		v.publisherRegions = make(map[string]string)
	}
	if region == "" {
		delete(v.publisherRegions, publisher)
		return
	}
	v.publisherRegions[publisher] = region
}

// regionFor returns the region to use for a given publisher, falling back to
// the default region if there's no override. The default region is also used
// when publisher is "google" or empty.
func (v *VertexProvider) regionFor(publisher string) string {
	if r, ok := v.publisherRegions[publisher]; ok && r != "" {
		return r
	}
	return v.region
}

// NewVertexProvider creates a new Vertex AI provider.
// This function constructs an authenticated HTTP client using Application Default Credentials
// scoped to the Google Cloud Platform.
func NewVertexProvider(ctx context.Context, projectID, region string) (*VertexProvider, error) {
	// Re-use standard Gemini default logic for client setup but augmented with GCP scopes.
	client, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("failed to load google application default credentials: %w", err)
	}

	return &VertexProvider{
		projectID: projectID,
		region:    region,
		client:    client,
	}, nil
}

// NewVertexProviderFromJSON builds a VertexProvider from the raw bytes of a
// service-account JSON key file. This is the path used by the admin panel:
// the operator pastes/uploads the JSON, we store it encrypted in the database,
// and at load time we decrypt and pass the bytes in here — no reliance on the
// GOOGLE_APPLICATION_CREDENTIALS env var.
func NewVertexProviderFromJSON(ctx context.Context, projectID, region string, credsJSON []byte) (*VertexProvider, error) {
	creds, err := google.CredentialsFromJSON(ctx, credsJSON, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("vertex: parse credentials json: %w", err)
	}
	// Use context.Background for the OAuth2 HTTP client so the token source
	// can refresh tokens even after the initial constructor context ends.
	// The constructor's ctx is only used for initial credential validation.
	client := oauth2.NewClient(context.Background(), creds.TokenSource)
	return &VertexProvider{
		projectID: projectID,
		region:    region,
		client:    client,
	}, nil
}

// Name returns "vertex".
func (v *VertexProvider) Name() string { return "vertex" }

// StreamCompletion sends a streaming request to the Vertex AI API.
func (v *VertexProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	publisher, modelName := parseVertexModel(req.Model)
	switch publisher {
	case "google":
		return v.streamGoogleCompletion(ctx, req, modelName)
	case "anthropic":
		return v.streamAnthropicCompletion(ctx, req, modelName)
	default:
		return v.streamOpenAICompatCompletion(ctx, req, publisher, modelName)
	}
}

func (v *VertexProvider) streamGoogleCompletion(ctx context.Context, req *CompletionRequest, modelName string) (<-chan StreamEvent, error) {
	body := v.buildRequest(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal vertex request: %w", err)
	}

	region := v.regionFor("google")
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:streamGenerateContent?alt=sse",
		region, v.projectID, region, modelName)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create vertex request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vertex request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Parse Retry-After header for rate limit responses
		headers := NormalizeHeaders(resp.Header)
		retryAfter := ExtractRetryAfterFromHeaders(headers)
		return nil, NewProviderError(resp.StatusCode, string(respBody), retryAfter)
	}

	ch := make(chan StreamEvent, 64)
	go v.processStream(resp.Body, ch)
	return ch, nil
}

func parseVertexModel(model string) (string, string) {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) == 1 || parts[0] == "gemini" || strings.HasPrefix(model, "gemini-") {
		return "google", model
	}
	return parts[0], parts[1]
}

func (v *VertexProvider) buildRequest(req *CompletionRequest) map[string]any {
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

	// Convert messages to Gemini's format
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

func (v *VertexProvider) processStream(body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var usage UsageInfo
	usage.Provider = "vertex"

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
				if val, ok := um["promptTokenCount"].(float64); ok {
					usage.InputTokens = int(val)
				}
				if val, ok := um["candidatesTokenCount"].(float64); ok {
					usage.OutputTokens = int(val)
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
			if val, ok := um["promptTokenCount"].(float64); ok {
				usage.InputTokens = int(val)
			}
			if val, ok := um["candidatesTokenCount"].(float64); ok {
				usage.OutputTokens = int(val)
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
