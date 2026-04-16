package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// bedrockModelsJSON is the curated list of AWS Bedrock foundation models
// available via the Converse API. AWS does not expose per-model pricing
// through any runtime API — the AWS Price List bulk feed returns SKU rows
// that are painful to map back to per-token costs — so every production LLM
// gateway (LiteLLM, Portkey, Helicone) ships a hand-curated table like this.
//
// Refresh this file when AWS announces price changes on
// https://aws.amazon.com/bedrock/pricing/. The version field in the JSON
// makes drift easy to detect.
//
//go:embed data/bedrock_models.json
var bedrockModelsJSON []byte

// bedrockCatalogFile is the on-disk shape of bedrock_models.json.
type bedrockCatalogFile struct {
	Version string                `json:"version"`
	Source  string                `json:"source"`
	Note    string                `json:"note,omitempty"`
	Models  []bedrockCatalogEntry `json:"models"`
}

// bedrockCatalogEntry holds one row from the curated file. Per-million prices
// are authored in the JSON for human readability; they are converted to
// per-token when materialized into ModelInfo / ModelPricing structs.
type bedrockCatalogEntry struct {
	ID                     string  `json:"id"`
	Name                   string  `json:"name"`
	ContextLength          int     `json:"context_length"`
	MaxOutputTokens        int     `json:"max_output_tokens"`
	SupportsTools          bool    `json:"supports_tools"`
	SupportsVision         bool    `json:"supports_vision"`
	InputUSDPerMillion     float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion    float64 `json:"output_usd_per_million"`
	CacheReadUSDPerMillion float64 `json:"cache_read_usd_per_million,omitempty"`
	CacheWriteUSDPerMillion float64 `json:"cache_write_usd_per_million,omitempty"`
}

// BedrockCatalogBytes returns the raw bytes of the curated bedrock_models.json
// file. Exposed so the admin panel can serve the exact file contents (including
// the version field) without re-marshalling.
func BedrockCatalogBytes() []byte {
	return bedrockModelsJSON
}

// BedrockCatalogVersion returns the version string declared in the curated
// file so operators can tell when Bedrock pricing was last refreshed.
func BedrockCatalogVersion() string {
	var f bedrockCatalogFile
	if err := json.Unmarshal(bedrockModelsJSON, &f); err != nil {
		return ""
	}
	return f.Version
}

// parseBedrockCatalog decodes the embedded JSON into two parallel maps keyed
// by model ID: one of ModelInfo suitable for the catalog, the other of
// ModelPricing suitable for the pricing service. Shared by catalog.go and
// pricing.go so there is a single source of truth for Bedrock rates.
func parseBedrockCatalog() (map[string]ModelInfo, map[string]ModelPricing, error) {
	var f bedrockCatalogFile
	if err := json.Unmarshal(bedrockModelsJSON, &f); err != nil {
		return nil, nil, fmt.Errorf("parse bedrock_models.json: %w", err)
	}

	models := make(map[string]ModelInfo, len(f.Models))
	prices := make(map[string]ModelPricing, len(f.Models))

	for _, entry := range f.Models {
		if entry.ID == "" {
			continue
		}

		modalities := []string{"text"}
		if entry.SupportsVision {
			modalities = append(modalities, "image")
		}

		models[entry.ID] = ModelInfo{
			ID:              entry.ID,
			Name:            entry.Name,
			Source:          "bedrock",
			ContextLength:   entry.ContextLength,
			MaxOutputTokens: entry.MaxOutputTokens,
			PromptCost:      entry.InputUSDPerMillion / 1_000_000,
			CompletionCost:  entry.OutputUSDPerMillion / 1_000_000,
			SupportsTools:   entry.SupportsTools,
			InputModalities: modalities,
			Description:     fmt.Sprintf("AWS Bedrock — $%.2f/$%.2f per 1M tokens", entry.InputUSDPerMillion, entry.OutputUSDPerMillion),
		}

		prices[entry.ID] = ModelPricing{
			PromptCostPerToken:     entry.InputUSDPerMillion / 1_000_000,
			CompletionCostPerToken: entry.OutputUSDPerMillion / 1_000_000,
			CacheReadPerToken:      entry.CacheReadUSDPerMillion / 1_000_000,
			CacheWritePerToken:     entry.CacheWriteUSDPerMillion / 1_000_000,
		}
	}

	return models, prices, nil
}
