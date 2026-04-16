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

		pricing := ModelPricing{
			PromptCostPerToken:     entry.InputCostPerToken,
			CompletionCostPerToken: entry.OutputCostPerToken,
			CacheReadPerToken:      entry.CacheReadInput,
			CacheWritePerToken:     entry.CacheCreationInput,
		}

		var bujiIDs []string
		switch {
		case strings.HasPrefix(litellmID, "vertex_ai/"):
			bujiIDs = litellmToVertexIDs(litellmID)
			vertexCount++
		case strings.HasPrefix(litellmID, "bedrock/"):
			id := litellmToBedrockID(litellmID)
			if id != "" {
				bujiIDs = []string{id}
			}
			bedrockCount++
		default:
			continue // Only import Vertex + Bedrock from LiteLLM; other providers
			// use their own APIs (OpenRouter, Together, ZAI).
		}

		// Only set if not already present — OpenRouter/native API prices
		// take precedence over LiteLLM's static catalog.
		for _, bujiID := range bujiIDs {
			if _, exists := prices[bujiID]; !exists {
				prices[bujiID] = pricing
			}
		}
	}

	// Second pass: for Vertex Gemini models not found in the vertex_ai/ namespace,
	// fall back to the generic gemini/ or unprefixed pricing from LiteLLM.
	// Vertex AI Gemini uses the same per-token rates as the standard Gemini API.
	var fallbackCount int
	for litellmID, entry := range catalog {
		if entry.InputCostPerToken <= 0 && entry.OutputCostPerToken <= 0 {
			continue
		}
		// Only process gemini/ and unprefixed gemini- entries
		var modelName string
		switch {
		case strings.HasPrefix(litellmID, "gemini/"):
			modelName = strings.TrimPrefix(litellmID, "gemini/")
		case strings.HasPrefix(litellmID, "gemini-"):
			modelName = litellmID
		default:
			continue
		}

		vertexID := "vertex/" + modelName
		if _, exists := prices[vertexID]; !exists {
			prices[vertexID] = ModelPricing{
				PromptCostPerToken:     entry.InputCostPerToken,
				CompletionCostPerToken: entry.OutputCostPerToken,
				CacheReadPerToken:      entry.CacheReadInput,
				CacheWritePerToken:     entry.CacheCreationInput,
			}
			fallbackCount++

			// Also register stripped-version variant
			stripped := "vertex/" + stripVersionSuffix(modelName)
			if stripped != vertexID {
				if _, exists := prices[stripped]; !exists {
					prices[stripped] = prices[vertexID]
					fallbackCount++
				}
			}
		}
	}

	p.log.Info().
		Int("vertex_models", vertexCount).
		Int("bedrock_models", bedrockCount).
		Int("gemini_fallbacks", fallbackCount).
		Msg("merged LiteLLM pricing catalog")

	return nil
}

// litellmToVertexIDs converts a LiteLLM vertex_ai/ model ID to one or more
// BujiCoder vertex/ IDs. Returns multiple IDs to handle version-suffix
// variations (e.g. "gemini-2.0-flash-001" also registers as "gemini-2.0-flash").
func litellmToVertexIDs(litellmID string) []string {
	name := strings.TrimPrefix(litellmID, "vertex_ai/")

	// Strip @version suffixes (e.g. "claude-haiku-4-5@20251001" → "claude-haiku-4-5")
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}

	// Strip -maas suffix used by some Vertex partner models
	baseName := strings.TrimSuffix(name, "-maas")

	// Map Claude models to vertex/anthropic/ prefix
	makeID := func(n string) string {
		if strings.HasPrefix(n, "claude-") {
			return "vertex/anthropic/" + n
		}
		return "vertex/" + n
	}

	primary := makeID(baseName)
	ids := []string{primary}

	// Also register a stripped-version variant for Google models.
	// LiteLLM often has "gemini-2.0-flash-001" but Vertex catalog
	// lists "gemini-2.0-flash". Register both so pricing matches.
	if !strings.Contains(baseName, "/") { // Only for flat Google model names
		stripped := stripVersionSuffix(baseName)
		if stripped != baseName {
			ids = append(ids, makeID(stripped))
		}
	}

	return ids
}

// stripVersionSuffix removes trailing version numbers from model names.
// "gemini-2.0-flash-001" → "gemini-2.0-flash"
// "gemini-2.5-pro-preview-05-06" → "gemini-2.5-pro-preview"
// "claude-opus-4-6" → "claude-opus-4-6" (no change — not a version suffix)
func stripVersionSuffix(name string) string {
	// Match trailing -NNN or -YYMMDD patterns
	parts := strings.Split(name, "-")
	if len(parts) < 3 {
		return name
	}
	last := parts[len(parts)-1]
	// If last segment is all digits and looks like a version (001, 002, etc.)
	// or a date (20251001), strip it
	allDigits := true
	for _, c := range last {
		if c < '0' || c > '9' {
			allDigits = false
			break
		}
	}
	if allDigits && len(last) >= 2 {
		return strings.Join(parts[:len(parts)-1], "-")
	}
	return name
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
