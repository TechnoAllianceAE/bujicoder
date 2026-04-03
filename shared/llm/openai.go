package llm

import (
	"context"
	"time"
)

const openAIAPIURL = "https://api.openai.com/v1/chat/completions"

// OpenAIProvider implements the Provider interface for OpenAI's API.
type OpenAIProvider struct {
	compat *openAICompatProvider
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(apiKey string, timeout ...time.Duration) *OpenAIProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &OpenAIProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       openAIAPIURL,
			APIKey:       apiKey,
			ProviderName: "openai",
			Timeout:      t,
		}),
	}
}

// Name returns "openai".
func (o *OpenAIProvider) Name() string { return "openai" }

// StreamCompletion sends a streaming request to the OpenAI API.
func (o *OpenAIProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return o.compat.streamCompletion(ctx, req)
}
