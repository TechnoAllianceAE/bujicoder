package llm

import (
	"context"
	"strings"
	"time"
)

const defaultLlamaCppURL = "http://localhost:8080"

// LlamaCppProvider implements the Provider interface for llama.cpp's OpenAI-compatible API.
type LlamaCppProvider struct {
	compat *openAICompatProvider
}

// NewLlamaCppProvider creates a new llama.cpp provider. Cost is always 0 for local models.
func NewLlamaCppProvider(baseURL string, timeout ...time.Duration) *LlamaCppProvider {
	if baseURL == "" {
		baseURL = defaultLlamaCppURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &LlamaCppProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       baseURL + "/v1/chat/completions",
			ProviderName: "llamacpp",
			ZeroCost:     true,
			Timeout:      t,
		}),
	}
}

// Name returns "llamacpp".
func (l *LlamaCppProvider) Name() string { return "llamacpp" }

// StreamCompletion sends a streaming request to the llama.cpp API.
func (l *LlamaCppProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return l.compat.streamCompletion(ctx, req)
}
