package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (v *VertexProvider) streamAnthropicCompletion(ctx context.Context, req *CompletionRequest, modelName string) (<-chan StreamEvent, error) {
	anthropic := NewAnthropicProvider("")
	body := anthropic.buildRequest(&CompletionRequest{
		Model:          modelName,
		Messages:       req.Messages,
		Tools:          req.Tools,
		SystemPrompt:   req.SystemPrompt,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		ResponseFormat: req.ResponseFormat,
	})
	return v.streamRawPredict(ctx, "anthropic", modelName, body, anthropic.processStream)
}

func (v *VertexProvider) streamOpenAICompatCompletion(ctx context.Context, req *CompletionRequest, publisher, modelName string) (<-chan StreamEvent, error) {
	compat := newOpenAICompatProvider(OpenAICompatConfig{ProviderName: "vertex"})
	body := compat.buildRequest(&CompletionRequest{
		Model:          modelName,
		Messages:       req.Messages,
		Tools:          req.Tools,
		SystemPrompt:   req.SystemPrompt,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		ResponseFormat: req.ResponseFormat,
	})
	return v.streamRawPredict(ctx, publisher, modelName, body, compat.processStream)
}

func (v *VertexProvider) streamRawPredict(
	ctx context.Context,
	publisher string,
	modelName string,
	payload map[string]any,
	streamProcessor func(io.ReadCloser, chan<- StreamEvent),
) (<-chan StreamEvent, error) {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal vertex rawPredict request: %w", err)
	}

	requestBody := map[string]any{
		"httpBody": map[string]any{
			"contentType": "application/json",
			"data":        jsonBody,
		},
	}
	envelope, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshal vertex rawPredict envelope: %w", err)
	}

	region := v.regionFor(publisher)
	url := fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/%s/models/%s:streamRawPredict",
		vertexHostForRegion(region), v.projectID, region, publisher, modelName,
	)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(envelope))
	if err != nil {
		return nil, fmt.Errorf("create vertex rawPredict request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vertex rawPredict request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		headers := NormalizeHeaders(resp.Header)
		retryAfter := ExtractRetryAfterFromHeaders(headers)
		return nil, NewProviderError(resp.StatusCode, string(respBody), retryAfter)
	}

	ch := make(chan StreamEvent, 64)
	go streamProcessor(resp.Body, ch)
	return ch, nil
}
