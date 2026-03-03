package llm

import (
	"context"
)

const cerebrasAPIURL = "https://api.cerebras.ai/v1/chat/completions"

// CerebrasProvider implements the Provider interface for Cerebras' API.
type CerebrasProvider struct {
	compat *openAICompatProvider
}

// NewCerebrasProvider creates a new Cerebras provider.
func NewCerebrasProvider(apiKey string) *CerebrasProvider {
	return &CerebrasProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       cerebrasAPIURL,
			APIKey:       apiKey,
			ProviderName: "cerebras",
		}),
	}
}

// Name returns "cerebras".
func (c *CerebrasProvider) Name() string { return "cerebras" }

// StreamCompletion sends a streaming request to the Cerebras API.
func (c *CerebrasProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return c.compat.streamCompletion(ctx, req)
}
