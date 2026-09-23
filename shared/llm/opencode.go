package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/TechnoAllianceAE/bujicoder/shared/buildinfo"
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
			RequestHeaders:    opencodeHeaders,
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

// APIKey returns the provider's API key, for callers that hit other
// OpenCode endpoints (e.g. /v1/systemone) with the same credentials.
func (c *OpenCodeProvider) APIKey() string { return c.compat.cfg.APIKey }

// StreamCompletion sends a streaming request to the OpenCode Zen API.
func (c *OpenCodeProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	return c.compat.streamCompletion(ctx, req)
}

// opencodeHeaders identifies the client the way OpenCode requires: its own
// user agent (generic HTTP-library UAs are refused) and a stable
// x-opencode-session per conversation. The Go tier rejects requests without
// the session header (400 MissingSessionID); it also drives routing and
// prompt caching, so it's derived from the user plus the conversation's first
// message rather than a per-request ID.
func opencodeHeaders(req *CompletionRequest) map[string]string {
	return map[string]string{
		"User-Agent":         "bujicoder/" + buildinfo.Version,
		"x-opencode-session": opencodeSession(req),
	}
}

func opencodeSession(req *CompletionRequest) string {
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		for _, p := range m.Content {
			if p.Type == "text" && p.Text != "" {
				sum := sha256.Sum256([]byte(req.UserID + "\x00" + p.Text))
				return "bc-" + hex.EncodeToString(sum[:12])
			}
		}
		break
	}
	if req.RequestID != "" {
		return req.RequestID
	}
	return "bc-anon"
}
