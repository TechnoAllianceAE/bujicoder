package llm

import (
	"context"
)

const xaiAPIURL = "https://api.x.ai/v1/chat/completions"

// XAIProvider implements the Provider interface for XAI/Grok's API.
type XAIProvider struct {
	compat *openAICompatProvider
}

// NewXAIProvider creates a new XAI provider.
func NewXAIProvider(apiKey string) *XAIProvider {
	return &XAIProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       xaiAPIURL,
			APIKey:       apiKey,
			ProviderName: "x-ai",
		}),
	}
}

// Name returns "x-ai" to match the model prefix convention (e.g. "x-ai/grok-code-fast-1").
func (x *XAIProvider) Name() string { return "x-ai" }

// StreamCompletion sends a streaming request to the XAI API.
func (x *XAIProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return x.compat.streamCompletion(ctx, req)
}
