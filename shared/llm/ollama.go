package llm

import (
	"context"
	"strings"
)

const defaultOllamaURL = "http://localhost:11434"

// OllamaProvider implements the Provider interface for Ollama's OpenAI-compatible API.
type OllamaProvider struct {
	compat *openAICompatProvider
}

// NewOllamaProvider creates a new Ollama provider. Cost is always 0 for local models.
func NewOllamaProvider(baseURL string) *OllamaProvider {
	if baseURL == "" {
		baseURL = defaultOllamaURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return &OllamaProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       baseURL + "/v1/chat/completions",
			ProviderName: "ollama",
			ZeroCost:     true,
		}),
	}
}

// Name returns "ollama".
func (o *OllamaProvider) Name() string { return "ollama" }

// StreamCompletion sends a streaming request to the Ollama API.
func (o *OllamaProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return o.compat.streamCompletion(ctx, req)
}
