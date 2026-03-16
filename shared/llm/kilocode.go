package llm

import (
	"context"
)

const kilocodeGatewayURL = "https://api.kilo.ai/api/gateway/chat/completions"

// KilocodeProvider implements the Provider interface for the Kilo AI Gateway.
// The gateway is OpenAI-compatible and provides unified access to 500+ models
// across 30+ providers (Anthropic, OpenAI, Google, Mistral, xAI, DeepSeek, etc.)
// through a single API key.
//
// Usage:
//
//	Set KILOCODE_API_KEY env var or add kilocode: key in bujicoder.yaml
//	Models use "provider/model" format, e.g. "anthropic/claude-sonnet-4.5"
type KilocodeProvider struct {
	compat *openAICompatProvider
}

// NewKilocodeProvider creates a new Kilocode provider.
func NewKilocodeProvider(apiKey string) *KilocodeProvider {
	return &KilocodeProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       kilocodeGatewayURL,
			APIKey:       apiKey,
			ProviderName: "kilocode",
			ExtraHeaders: map[string]string{
				"HTTP-Referer": "https://bujicoder.com",
				"X-Title":      "BujiCoder",
			},
		}),
	}
}

// Name returns "kilocode".
func (k *KilocodeProvider) Name() string { return "kilocode" }

// StreamCompletion sends a streaming request through the Kilo AI Gateway.
func (k *KilocodeProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return k.compat.streamCompletion(ctx, req)
}
