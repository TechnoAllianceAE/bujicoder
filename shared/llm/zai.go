package llm

import (
	"context"
	"os"
	"time"
)

// Z.AI endpoint variants:
//   - Standard (pay-as-you-go):  /api/paas/v4/chat/completions
//   - Coding Plan (subscription): /api/coding/paas/v4/chat/completions
//
// Default to Coding Plan endpoint. Override with ZAI_API_URL env var.
const (
	zaiCodingURL  = "https://api.z.ai/api/coding/paas/v4/chat/completions"
	zaiPayGoURL   = "https://api.z.ai/api/paas/v4/chat/completions"
)

// ZAIProvider implements the Provider interface for Zhipu AI (GLM) models.
type ZAIProvider struct {
	compat *openAICompatProvider
}

// NewZAIProvider creates a new Zhipu AI provider.
// Uses the Coding Plan endpoint by default. Set ZAI_API_URL env var to override,
// or set ZAI_PAYGO=1 to use the pay-as-you-go endpoint.
func NewZAIProvider(apiKey string, timeout ...time.Duration) *ZAIProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	apiURL := zaiCodingURL
	if u := os.Getenv("ZAI_API_URL"); u != "" {
		apiURL = u
	} else if os.Getenv("ZAI_PAYGO") == "1" {
		apiURL = zaiPayGoURL
	}
	return &ZAIProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       apiURL,
			APIKey:       apiKey,
			ProviderName: "z-ai",
			Timeout:      t,
		}),
	}
}

// Name returns "z-ai".
func (z *ZAIProvider) Name() string { return "z-ai" }

// StreamCompletion sends a streaming request to the Zhipu AI API.
func (z *ZAIProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return z.compat.streamCompletion(ctx, req)
}
