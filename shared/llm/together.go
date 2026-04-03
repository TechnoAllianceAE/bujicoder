package llm

import (
	"context"
	"time"
)

const togetherAPIURL = "https://api.together.xyz/v1/chat/completions"

// TogetherProvider implements the Provider interface for Together AI's API.
type TogetherProvider struct {
	compat *openAICompatProvider
}

// NewTogetherProvider creates a new Together AI provider.
func NewTogetherProvider(apiKey string, timeout ...time.Duration) *TogetherProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &TogetherProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       togetherAPIURL,
			APIKey:       apiKey,
			ProviderName: "together",
			Timeout:      t,
		}),
	}
}

// Name returns "together".
func (t *TogetherProvider) Name() string { return "together" }

// StreamCompletion sends a streaming request to the Together AI API.
func (t *TogetherProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return t.compat.streamCompletion(ctx, req)
}
