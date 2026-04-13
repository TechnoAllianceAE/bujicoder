// Package llm provides the provider interface and adapters for LLM streaming.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// StreamEvent represents a single event in a streaming completion.
type StreamEvent struct {
	// Exactly one of these will be set.
	Delta    *DeltaEvent    `json:"delta,omitempty"`
	ToolCall *ToolCallEvent `json:"tool_call,omitempty"`
	Complete *CompleteEvent `json:"complete,omitempty"`
	Error    *ErrorEvent    `json:"error,omitempty"`
}

// DeltaEvent is an incremental text chunk.
type DeltaEvent struct {
	Text string `json:"text"`
}

// ToolCallEvent is a complete tool call from the model.
type ToolCallEvent struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ArgumentsJSON string `json:"arguments_json"`
}

// CompleteEvent signals the stream is finished.
type CompleteEvent struct {
	FinishReason string    `json:"finish_reason"`
	Usage        UsageInfo `json:"usage"`
}

// ErrorEvent signals a stream error.
type ErrorEvent struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// UsageInfo contains token usage and cost data.
type UsageInfo struct {
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	CostCents        int64  `json:"cost_cents"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
}

// Message represents a chat message.
type Message struct {
	Role    string        `json:"role"`
	Content []ContentPart `json:"content"`
}

// UnmarshalJSON handles both string and array content formats.
// The CLI sends content as a plain string, while tool-use messages use an array.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role

	if len(raw.Content) == 0 {
		return nil
	}

	// Try string first (simple messages from CLI).
	var text string
	if err := json.Unmarshal(raw.Content, &text); err == nil {
		m.Content = []ContentPart{{Type: "text", Text: text}}
		return nil
	}

	// Otherwise expect an array of ContentPart.
	var parts []ContentPart
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return fmt.Errorf("content must be a string or array of content parts: %w", err)
	}
	m.Content = parts
	return nil
}

// ContentPart is one part of a message's content.
type ContentPart struct {
	Type          string    `json:"type"`
	Text          string    `json:"text,omitempty"`
	ToolCallID    string    `json:"tool_call_id,omitempty"`
	ToolName      string    `json:"tool_name,omitempty"`
	ArgumentsJSON string    `json:"arguments_json,omitempty"`
	IsError       bool      `json:"is_error,omitempty"`
	ImageURL      *ImageURL `json:"image_url,omitempty"` // For vision: inline base64 or remote URL
}

// ImageURL represents an image attachment for multimodal messages.
type ImageURL struct {
	URL       string `json:"url"`        // HTTP URL or data URI (data:image/png;base64,...)
	MediaType string `json:"media_type"` // e.g. "image/png", "image/jpeg" — required by Anthropic
}

// ToolDefinition defines a tool available to the model.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ResponseFormat specifies structured output requirements for the LLM.
// When set, the model is instructed to return JSON conforming to the schema.
type ResponseFormat struct {
	Type   string         `json:"type"`   // "json_schema" or "json_object"
	Schema map[string]any `json:"schema"` // JSON Schema object (for "json_schema" type)
}

// CompletionRequest is the input to a streaming completion.
type CompletionRequest struct {
	RequestID      string
	UserID         string
	OrgID          *string
	Model          string
	Messages       []Message
	Tools          []ToolDefinition
	SystemPrompt   *string
	Temperature    *float32
	MaxTokens      *int
	CostMode       string          // Cost mode for model routing (normal, heavy, max)
	AgentID        string          // Agent ID for per-agent model overrides (e.g., "base", "editor")
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"` // When set, forces structured JSON output
	// OAuthToken, when set on an Anthropic request, uses Bearer auth charged against the
	// user's Claude subscription instead of the server-side API key (per-token billing).
	OAuthToken string
	// KiroToken, when set, uses AWS CodeWhisperer's token for authentication instead of the server API key.
	KiroToken string
}

// Provider is the interface that LLM provider adapters must implement.
type Provider interface {
	// StreamCompletion sends a request and returns a channel of streaming events.
	StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error)
	// Name returns the provider name (e.g., "anthropic", "openai").
	Name() string
}

// Registry maps provider names to Provider implementations.
type Registry struct {
	providers       map[string]Provider
	defaultProvider Provider
	catalog         *ModelCatalog
	pricing         *PricingService
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider to the registry.
func (r *Registry) Register(provider Provider) {
	r.providers[provider.Name()] = provider
}

// Unregister removes a provider from the registry by name.
func (r *Registry) Unregister(name string) {
	delete(r.providers, name)
}

// HasProviders returns true if at least one provider or a default fallback is registered.
func (r *Registry) HasProviders() bool {
	return len(r.providers) > 0 || r.defaultProvider != nil
}

// SetDefault sets a fallback provider used when no named provider matches.
// The CLI uses this to route all LLM calls through the gateway proxy.
func (r *Registry) SetDefault(p Provider) {
	r.defaultProvider = p
}

// SetCatalog attaches a model catalog for validation during routing.
func (r *Registry) SetCatalog(catalog *ModelCatalog) {
	r.catalog = catalog
}

// GetCatalog returns the attached model catalog, or nil if none is set.
func (r *Registry) GetCatalog() *ModelCatalog {
	return r.catalog
}

// SetPricing attaches a pricing service for cost calculation.
func (r *Registry) SetPricing(p *PricingService) {
	r.pricing = p
}

// GetPricing returns the attached pricing service, or nil if none is set.
func (r *Registry) GetPricing() *PricingService {
	return r.pricing
}

// Route determines the provider for a given model string (e.g., "openai/gpt-oss-120b:free").
// If the model's prefix provider is directly registered, it is used.
// Otherwise, the request is routed through OpenRouter with the full model ID.
// The model catalog (models.json) is used only for the admin UI listing;
// it does not restrict which models can be used for completions.
func (r *Registry) Route(model string) (Provider, string, error) {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid model format %q: expected 'provider/model'", model)
	}

	providerName := parts[0]
	modelName := parts[1]

	if provider, ok := r.providers[providerName]; ok {
		return provider, modelName, nil
	}

	// Fall back to default provider (e.g. gateway proxy in CLI mode).
	if r.defaultProvider != nil {
		return r.defaultProvider, model, nil
	}

	if orProvider, ok := r.providers["openrouter"]; ok {
		return orProvider, model, nil
	}

	return nil, "", fmt.Errorf("unknown provider %q and no openrouter fallback available", providerName)
}
