package llm

import (
	"context"
	"time"
)

// OpenCode Zen exposes two OpenAI-compatible chat-completions endpoints, one
// per subscription tier. They serve different model sets, so each is wired as
// its own bujicoder provider:
//   - opencodeGoAPIURL  ("/zen/go/") = the "Go" tier (kimi-k2.6, glm-5.1, ...)
//   - opencodeZenAPIURL ("/zen/")    = the base Zen tier (big-pickle, *-free)
//
// Models are referenced verbatim by id behind the "opencode/" (Go) or
// "opencode-zen/" (Zen) route prefix.
const (
	opencodeGoAPIURL  = "https://opencode.ai/zen/go/v1/chat/completions"
	opencodeZenAPIURL = "https://opencode.ai/zen/v1/chat/completions"
)

// OpenCodeProvider implements the Provider interface for OpenCode Zen's
// OpenAI-compatible API. The same type serves both tiers; name + endpoint
// distinguish them.
type OpenCodeProvider struct {
	name   string
	compat *openAICompatProvider
}

func newOpenCode(name, apiURL, apiKey string, timeout ...time.Duration) *OpenCodeProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &OpenCodeProvider{
		name: name,
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:            apiURL,
			APIKey:            apiKey,
			ProviderName:      name,
			Timeout:           t,
			SupportsReasoning: true,
		}),
	}
}

// NewOpenCodeProvider creates a new OpenCode Zen "Go" tier provider.
func NewOpenCodeProvider(apiKey string, timeout ...time.Duration) *OpenCodeProvider {
	return newOpenCode("opencode", opencodeGoAPIURL, apiKey, timeout...)
}

// NewOpenCodeZenProvider creates a new OpenCode Zen base-tier provider, which
// serves zen-only models such as "big-pickle".
func NewOpenCodeZenProvider(apiKey string, timeout ...time.Duration) *OpenCodeProvider {
	return newOpenCode("opencode-zen", opencodeZenAPIURL, apiKey, timeout...)
}

// Name returns the provider name ("opencode" or "opencode-zen").
func (c *OpenCodeProvider) Name() string { return c.name }

// StreamCompletion sends a streaming request to the OpenCode Zen API.
func (c *OpenCodeProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return c.compat.streamCompletion(ctx, req)
}
