package llm

import (
	"context"
	"fmt"
	"strings"
)

// AzureOpenAIProvider implements the Provider interface for Azure OpenAI Service.
// Azure uses per-deployment URLs and an "api-key" header instead of "Authorization: Bearer".
//
// The model field in a CompletionRequest is interpreted as the Azure deployment name,
// e.g. model "azure/my-gpt-4o-deployment" → deployment "my-gpt-4o-deployment".
type AzureOpenAIProvider struct {
	apiKey     string
	endpoint   string // e.g. https://myresource.openai.azure.com
	apiVersion string // e.g. 2024-10-21
}

// NewAzureOpenAIProvider creates a new Azure OpenAI provider.
// endpoint may be the full URL ("https://myresource.openai.azure.com") or just the resource name ("myresource").
// apiVersion defaults to "2024-10-21" if empty.
func NewAzureOpenAIProvider(apiKey, endpoint, apiVersion string) *AzureOpenAIProvider {
	if apiVersion == "" {
		apiVersion = "2024-10-21"
	}
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
		endpoint = fmt.Sprintf("https://%s.openai.azure.com", endpoint)
	}
	endpoint = strings.TrimRight(endpoint, "/")
	return &AzureOpenAIProvider{
		apiKey:     apiKey,
		endpoint:   endpoint,
		apiVersion: apiVersion,
	}
}

// Name returns "azure".
func (a *AzureOpenAIProvider) Name() string { return "azure" }

// StreamCompletion sends a streaming request to the Azure OpenAI deployment named by req.Model.
func (a *AzureOpenAIProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	deployment := req.Model
	if deployment == "" {
		return nil, fmt.Errorf("azure: model (deployment name) is required")
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		a.endpoint, deployment, a.apiVersion)

	compat := newOpenAICompatProvider(OpenAICompatConfig{
		APIURL:       url,
		APIKey:       "", // leave blank so the compat provider skips Authorization header
		ProviderName: "azure",
		ExtraHeaders: map[string]string{"api-key": a.apiKey},
	})
	return compat.streamCompletion(ctx, req)
}
