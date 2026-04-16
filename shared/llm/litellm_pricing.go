package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const litellmPricingURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// litellmModelEntry is a subset of the LiteLLM pricing JSON schema.
type litellmModelEntry struct {
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	CacheReadInput     float64 `json:"cache_read_input_token_cost"`
	CacheCreationInput float64 `json:"cache_creation_input_token_cost"`
}

// mergeLiteLLMPricing fetches comprehensive model pricing from LiteLLM's
// public catalog and merges Vertex AI, Bedrock, and other provider prices
// into the price map. LiteLLM's catalog is community-maintained and covers
// 100+ Vertex models and 50+ Bedrock models with per-token USD rates sourced
// from official provider pricing pages.
//
// This replaces fragile HTML-scraping of Google's pricing page and provides
// pricing for all Vertex third-party models (Claude, Mistral, Llama, etc.)
// that the scraper could never discover.
func (p *PricingService) mergeLiteLLMPricing(ctx context.Context, prices map[string]ModelPricing) error {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, litellmPricingURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch litellm pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("litellm pricing status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB limit
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var catalog map[string]litellmModelEntry
	if err := json.Unmarshal(body, &catalog); err != nil {
		return fmt.Errorf("decode litellm pricing: %w", err)
	}

	var vertexCount, bedrockCount int
	for litellmID, entry := range catalog {
		if entry.InputCostPerToken <= 0 && entry.OutputCostPerToken <= 0 {
			continue
		}

		var bujiID string
		switch {
		case strings.HasPrefix(litellmID, "vertex_ai/"):
			bujiID = litellmToVertexID(litellmID)
			vertexCount++
		case strings.HasPrefix(litellmID, "bedrock/"):
			bujiID = litellmToBedrockID(litellmID)
			bedrockCount++
		default:
			continue // Only import Vertex + Bedrock from LiteLLM; other providers
			// use their own APIs (OpenRouter, Together, ZAI).
		}

		if bujiID == "" {
			continue
		}

		pricing := ModelPricing{
			PromptCostPerToken:     entry.InputCostPerToken,
			CompletionCostPerToken: entry.OutputCostPerToken,
			CacheReadPerToken:      entry.CacheReadInput,
			CacheWritePerToken:     entry.CacheCreationInput,
		}

		// Only set if not already present — OpenRouter/native API prices
		// take precedence over LiteLLM's static catalog.
		if _, exists := prices[bujiID]; !exists {
			prices[bujiID] = pricing
		}
	}

	p.log.Info().
		Int("vertex_models", vertexCount).
		Int("bedrock_models", bedrockCount).
		Msg("merged LiteLLM pricing catalog")

	return nil
}

// litellmToVertexID converts a LiteLLM vertex_ai/ model ID to BujiCoder's
// vertex/ naming convention.
//
// LiteLLM format: "vertex_ai/claude-sonnet-4-6", "vertex_ai/gemini-2.0-flash-001"
// BujiCoder format: "vertex/anthropic/claude-sonnet-4-6", "vertex/gemini-2.0-flash-001"
//
// LiteLLM uses "vertex_ai/" prefix for all Vertex models, with some using
// publisher sub-prefixes (e.g. "vertex_ai/meta/llama-4-scout-...") and some
// using flat names (e.g. "vertex_ai/claude-opus-4-6").
func litellmToVertexID(litellmID string) string {
	name := strings.TrimPrefix(litellmID, "vertex_ai/")

	// Strip @version suffixes (e.g. "claude-haiku-4-5@20251001" → "claude-haiku-4-5")
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}

	// LiteLLM already uses publisher/ prefix for non-Google models
	// (e.g. "meta/llama-4-scout", "deepseek-ai/deepseek-v3.1-maas")
	// but uses flat names for Claude (e.g. "claude-opus-4-6").
	// Map known Claude/Anthropic models to vertex/anthropic/ prefix.
	if strings.HasPrefix(name, "claude-") {
		return "vertex/anthropic/" + name
	}

	return "vertex/" + name
}

// litellmToBedrockID converts a LiteLLM bedrock/ model ID to BujiCoder format.
// LiteLLM and BujiCoder use the same "bedrock/" prefix, but LiteLLM may
// include region suffixes or inference profile names that need stripping.
func litellmToBedrockID(litellmID string) string {
	name := strings.TrimPrefix(litellmID, "bedrock/")

	// Skip inference profiles and region-specific entries
	// (e.g. "bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0")
	if strings.Contains(name, ".") && !strings.HasPrefix(name, "ai21.") &&
		!strings.HasPrefix(name, "amazon.") && !strings.HasPrefix(name, "anthropic.") &&
		!strings.HasPrefix(name, "meta.") && !strings.HasPrefix(name, "mistral.") &&
		!strings.HasPrefix(name, "cohere.") {
		return ""
	}

	return "bedrock/" + name
}
