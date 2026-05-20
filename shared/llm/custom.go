package llm

import (
	"context"
	"strings"
	"time"
)

// CustomOpenAIProvider wraps any OpenAI-compatible endpoint at a user-supplied
// base URL. The base URL may be the chat-completions endpoint itself or a
// shorter base (e.g. "https://api.moonshot.cn/v1") — in the latter case the
// canonical "/chat/completions" suffix is appended.
type CustomOpenAIProvider struct {
	name   string
	compat *openAICompatProvider
}

// NewCustomOpenAIProvider creates a provider for an arbitrary OpenAI-compatible
// API. name is the registry identifier (e.g. "moonshot").
func NewCustomOpenAIProvider(name, baseURL, apiKey string, timeout ...time.Duration) *CustomOpenAIProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if url != "" && !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	return &CustomOpenAIProvider{
		name: name,
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       url,
			APIKey:       apiKey,
			ProviderName: name,
			Timeout:      t,
		}),
	}
}

func (p *CustomOpenAIProvider) Name() string { return p.name }

func (p *CustomOpenAIProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return p.compat.streamCompletion(ctx, req)
}
