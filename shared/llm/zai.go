package llm

import (
	"context"
)

const zaiAPIURL = "https://api.z.ai/api/paas/v4/chat/completions"

// ZAIProvider implements the Provider interface for Zhipu AI (GLM) models.
type ZAIProvider struct {
	compat *openAICompatProvider
}

// NewZAIProvider creates a new Zhipu AI provider.
func NewZAIProvider(apiKey string) *ZAIProvider {
	return &ZAIProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       zaiAPIURL,
			APIKey:       apiKey,
			ProviderName: "z-ai",
		}),
	}
}

// Name returns "z-ai".
func (z *ZAIProvider) Name() string { return "z-ai" }

// StreamCompletion sends a streaming request to the Zhipu AI API.
func (z *ZAIProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return z.compat.streamCompletion(ctx, req)
}
