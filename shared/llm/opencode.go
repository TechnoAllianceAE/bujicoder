package llm

import (
	"context"
	"time"
)

// opencodeAPIURL is the OpenCode Zen OpenAI-compatible chat completions
// endpoint. The "/go/" segment is the Zen "Go" subscription tier. Models are
// referenced verbatim by id (e.g. "kimi-k2.6", "glm-5.1") behind the
// "opencode/" route prefix.
const opencodeAPIURL = "https://opencode.ai/zen/go/v1/chat/completions"

// OpenCodeProvider implements the Provider interface for OpenCode Zen's
// OpenAI-compatible API.
type OpenCodeProvider struct {
	compat *openAICompatProvider
}

// NewOpenCodeProvider creates a new OpenCode Zen provider.
func NewOpenCodeProvider(apiKey string, timeout ...time.Duration) *OpenCodeProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &OpenCodeProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:            opencodeAPIURL,
			APIKey:            apiKey,
			ProviderName:      "opencode",
			Timeout:           t,
			SupportsReasoning: true,
		}),
	}
}

// Name returns "opencode".
func (c *OpenCodeProvider) Name() string { return "opencode" }

// StreamCompletion sends a streaming request to the OpenCode Zen API.
func (c *OpenCodeProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return c.compat.streamCompletion(ctx, req)
}
