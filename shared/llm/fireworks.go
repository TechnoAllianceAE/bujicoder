package llm

import (
	"context"
	"time"
)

const fireworksAPIURL = "https://api.fireworks.ai/inference/v1/chat/completions"

// FireworksProvider implements the Provider interface for Fireworks AI's API.
type FireworksProvider struct {
	compat *openAICompatProvider
}

// NewFireworksProvider creates a new Fireworks AI provider.
func NewFireworksProvider(apiKey string, timeout ...time.Duration) *FireworksProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &FireworksProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       fireworksAPIURL,
			APIKey:       apiKey,
			ProviderName: "fireworks",
			Timeout:      t,
		}),
	}
}

// Name returns "fireworks".
func (f *FireworksProvider) Name() string { return "fireworks" }

// StreamCompletion sends a streaming request to the Fireworks AI API.
func (f *FireworksProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return f.compat.streamCompletion(ctx, req)
}
