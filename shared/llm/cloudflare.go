package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// cloudflareAPITemplate is the OpenAI-compatible chat completions endpoint for
// Cloudflare Workers AI. Cloudflare requires the account ID to be baked into
// the URL path — there is no header-based account selector.
const cloudflareAPITemplate = "https://api.cloudflare.com/client/v4/accounts/%s/ai/v1/chat/completions"

// CloudflareProvider implements the Provider interface for Cloudflare Workers
// AI. Model IDs use Cloudflare's "@cf/<publisher>/<model>" form, e.g.
// "@cf/meta/llama-3.1-8b-instruct".
type CloudflareProvider struct {
	compat    *openAICompatProvider
	accountID string
}

// NewCloudflareProvider creates a Cloudflare Workers AI provider. apiToken is
// a scoped API token with "Workers AI - Read/Run" permission; accountID is the
// Cloudflare account ID (a 32-char hex string visible in the dashboard URL).
func NewCloudflareProvider(apiToken, accountID string, timeout ...time.Duration) *CloudflareProvider {
	var t time.Duration
	if len(timeout) > 0 {
		t = timeout[0]
	}
	accountID = strings.TrimSpace(accountID)
	return &CloudflareProvider{
		accountID: accountID,
		compat: newOpenAICompatProvider(OpenAICompatConfig{
			APIURL:       fmt.Sprintf(cloudflareAPITemplate, accountID),
			APIKey:       apiToken,
			ProviderName: "cloudflare",
			Timeout:      t,
		}),
	}
}

// Name returns "cloudflare".
func (c *CloudflareProvider) Name() string { return "cloudflare" }

// StreamCompletion sends a streaming request to Cloudflare Workers AI.
func (c *CloudflareProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return c.compat.streamCompletion(ctx, req)
}
