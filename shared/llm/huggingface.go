package llm

import (
	"context"
	"time"
)

// huggingFaceAPIURL is the OpenAI-compatible chat completions endpoint exposed
// by HuggingFace's Inference Providers router. It transparently dispatches to
// the configured inference provider for a given model ID (e.g. "meta-llama/
// Meta-Llama-3-8B-Instruct").
const huggingFaceAPIURL = "https://router.huggingface.co/v1/chat/completions"

// HuggingFaceProvider implements the Provider interface for HuggingFace
// Inference Providers via the OpenAI-compatible router endpoint.
type HuggingFaceProvider struct {
	compat *openAICompatProvider
}

// NewHuggingFaceProvider creates a HuggingFace inference provider. apiToken is
// a user access token (hf_...) with "Make calls to Inference Providers"
// permission.
func NewHuggingFaceProvider(apiToken string, timeout ...time.Duration) *HuggingFaceProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	return &HuggingFaceProvider{
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       huggingFaceAPIURL,
			APIKey:       apiToken,
			ProviderName: "huggingface",
			Timeout:      t,
		}),
	}
}

// Name returns "huggingface".
func (h *HuggingFaceProvider) Name() string { return "huggingface" }

// StreamCompletion sends a streaming request to the HuggingFace router.
func (h *HuggingFaceProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return h.compat.streamCompletion(ctx, req)
}
