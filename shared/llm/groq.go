package llm

import (
	"context"
	"time"
)

const groqAPIURL = "https://api.groq.com/openai/v1/chat/completions"

// GroqProvider implements the Provider interface for Groq's API.
type GroqProvider struct {
	compat *openAICompatProvider
}

// NewGroqProvider creates a new Groq provider.
func NewGroqProvider(apiKey string, timeout ...time.Duration) *GroqProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &GroqProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       groqAPIURL,
			APIKey:       apiKey,
			ProviderName: "groq",
			Timeout:      t,
		}),
	}
}

// Name returns "groq".
func (g *GroqProvider) Name() string { return "groq" }

// StreamCompletion sends a streaming request to the Groq API.
func (g *GroqProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return g.compat.streamCompletion(ctx, req)
}
