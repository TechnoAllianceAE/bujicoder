package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"time"
)

// OpenCode Zen exposes OpenAI-compatible chat-completions endpoints, one per
// subscription tier. They serve different model sets, so each is wired as its
// own bujicoder provider ("opencode" = Go tier, "opencode-zen" = base tier).
//
// Models are referenced verbatim by id behind the "opencode/" (Go) or
// "opencode-zen/" (Zen) route prefix.
//
// Endpoint layout (tier base + version + path) is configurable so a future
// Zen API version only needs env, not a code change:
//
//	OPENCODE_ZEN_BASE_URL  default https://opencode.ai/zen
//	OPENCODE_GO_BASE_URL   default https://opencode.ai/zen/go
//	OPENCODE_API_VERSION   default v1
const (
	defaultZenBase    = "https://opencode.ai/zen"
	defaultGoBase     = "https://opencode.ai/zen/go"
	defaultAPIVersion = "v1"
)

// opencodeUserAgent mirrors the opencode CLI's own User-Agent. Zen identifies
// official clients by it (generic HTTP-library UAs are refused, and requests
// without the client headers are treated as anonymous free-tier traffic).
// Override with OPENCODE_USER_AGENT when the CLI string moves on.
const opencodeUserAgent = "opencode/1.18.34 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14"

// opencodeClient is the x-opencode-client value the CLI sends.
const opencodeClient = "cli"

// OpenCodeUserAgent returns the User-Agent sent to Zen.
func OpenCodeUserAgent() string {
	if ua := strings.TrimSpace(os.Getenv("OPENCODE_USER_AGENT")); ua != "" {
		return ua
	}
	return opencodeUserAgent
}

// OpenCodeAPIVersion returns the Zen API version path segment (default "v1").
func OpenCodeAPIVersion() string {
	if v := strings.Trim(os.Getenv("OPENCODE_API_VERSION"), " /"); v != "" {
		return v
	}
	return defaultAPIVersion
}

// zenBase returns the tier base URL ("go" or anything else = base Zen tier).
func zenBase(tier string) string {
	if tier == "go" {
		if b := strings.TrimRight(strings.TrimSpace(os.Getenv("OPENCODE_GO_BASE_URL")), "/"); b != "" {
			return b
		}
		return defaultGoBase
	}
	if b := strings.TrimRight(strings.TrimSpace(os.Getenv("OPENCODE_ZEN_BASE_URL")), "/"); b != "" {
		return b
	}
	return defaultZenBase
}

// OpenCodeChatURL returns the chat-completions URL for a tier ("go" or "zen").
func OpenCodeChatURL(tier string) string {
	return zenBase(tier) + "/" + OpenCodeAPIVersion() + "/chat/completions"
}

// OpenCodeModelsURL returns the models-listing URL for a tier ("go" or "zen").
func OpenCodeModelsURL(tier string) string {
	return zenBase(tier) + "/" + OpenCodeAPIVersion() + "/models"
}

// OpenCodeSystemOneURL returns the Jev systemone URL for a tier ("go" or "zen").
func OpenCodeSystemOneURL(tier string) string {
	return zenBase(tier) + "/" + OpenCodeAPIVersion() + "/systemone"
}

// OpenCodeClientHeaders returns the headers Zen expects from official clients:
// User-Agent, x-opencode-client, a fresh x-opencode-request-id per call, and
// the caller's x-opencode-session (stable per conversation; drives Zen routing
// and prompt caching).
func OpenCodeClientHeaders(session string) map[string]string {
	return map[string]string{
		"User-Agent":            OpenCodeUserAgent(),
		"x-opencode-client":     opencodeClient,
		"x-opencode-request-id": newOpenCodeRequestID(),
		"x-opencode-session":    session,
	}
}

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
	return newOpenCode("opencode", OpenCodeChatURL("go"), apiKey, timeout...)
}

// NewOpenCodeZenProvider creates a new OpenCode Zen base-tier provider, which
// serves zen-only models such as "big-pickle".
func NewOpenCodeZenProvider(apiKey string, timeout ...time.Duration) *OpenCodeProvider {
	return newOpenCode("opencode-zen", OpenCodeChatURL("zen"), apiKey, timeout...)
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

// opencodeHeaders identifies the client the way Zen requires: the official
// User-Agent plus x-opencode-client / x-opencode-request-id, and a stable
// x-opencode-session per conversation. The Go tier rejects requests without
// the session header (400 MissingSessionID); it also drives routing and
// prompt caching, so it's derived from the user plus the conversation's first
// message rather than a per-request ID.
func opencodeHeaders(req *CompletionRequest) map[string]string {
	return OpenCodeClientHeaders(opencodeSession(req))
}

func opencodeSession(req *CompletionRequest) string {
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		for _, p := range m.Content {
			if p.Type == "text" && p.Text != "" {
				sum := sha256.Sum256([]byte(req.UserID + "\x00" + p.Text))
				return "ses_" + hex.EncodeToString(sum[:12])
			}
		}
		break
	}
	if req.RequestID != "" {
		return req.RequestID
	}
	return "ses_anon"
}

// newOpenCodeRequestID returns a CLI-shaped per-request id ("msg_" + 24
// alphanumerics, e.g. msg_0f6a63bc3001rXUDQmbLzVC6Wg).
func newOpenCodeRequestID() string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		sum := sha256.Sum256([]byte(time.Now().String()))
		return "msg_" + hex.EncodeToString(sum[:12])
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "msg_" + string(b)
}
