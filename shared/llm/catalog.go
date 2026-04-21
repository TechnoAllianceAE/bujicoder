package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ModelInfo holds metadata about an available model.
type ModelInfo struct {
	ID              string   `json:"id"`
	Name                string   `json:"name"`
	Source              string   `json:"source"`
	ContextLength       int      `json:"context_length"`
	MaxOutputTokens     int      `json:"max_output_tokens,omitempty"`
	PromptCost          float64  `json:"prompt_cost"`
	CompletionCost      float64  `json:"completion_cost"`
	Created             int64    `json:"created,omitempty"`
	InputModalities     []string `json:"input_modalities,omitempty"`
	SupportedParams     []string `json:"supported_parameters,omitempty"`
	SupportsTools       bool     `json:"supports_tools,omitempty"`
	Description         string   `json:"description,omitempty"`
	KnowledgeCutoff     string   `json:"knowledge_cutoff,omitempty"`
}

type modelEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ContextLength int    `json:"context_length"`
	Created       int64  `json:"created"`
	TopProvider   struct {
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	Architecture struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	SupportedParams []string `json:"supported_parameters"`
	KnowledgeCutoff *string  `json:"knowledge_cutoff"`
}

type modelsFile struct {
	Data []modelEntry `json:"data"`
}

// togetherModelEntry matches the Together AI /v1/models JSON shape.
type togetherModelEntry struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	DisplayName   string `json:"display_name"`
	ContextLength int    `json:"context_length"`
	Created       int64  `json:"created"`
	Pricing       struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"pricing"`
}

// zaiModelEntry matches the Z.AI /api/paas/v4/models JSON shape.
type zaiModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type zaiModelsResponse struct {
	Data []zaiModelEntry `json:"data"`
}

// zaiDefaultPricing provides pricing for Z.AI models in USD per million tokens.
// All plan users: GLM-5.1, GLM-5-Turbo, GLM-4.7, GLM-4.6, GLM-4.5-Air
// Max/Pro plan users additionally: GLM-5
// Source: https://z.ai/model-api
var zaiDefaultPricing = map[string]struct{ Input, Output float64 }{
	"glm-5.1":     {Input: 1.00, Output: 3.00},
	"glm-5-turbo": {Input: 0.50, Output: 1.50},
	"glm-5":       {Input: 0.95, Output: 2.55},
	"glm-4.7":     {Input: 0.30, Output: 1.40},
	"glm-4.6":     {Input: 0.35, Output: 1.71},
	"glm-4.5":     {Input: 0.55, Output: 2.00},
	"glm-4.5-air": {Input: 0.13, Output: 0.85},
}

const zaiDefaultContextLength = 128000

// fireworksModelEntry matches the Fireworks AI /v1/models JSON shape (OpenAI-compat).
type fireworksModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type fireworksModelsResponse struct {
	Object string                `json:"object"`
	Data   []fireworksModelEntry `json:"data"`
}

// fireworksDefaultPricing provides pricing for popular Fireworks AI models
// in USD per million tokens. Source: https://fireworks.ai/pricing
var fireworksDefaultPricing = map[string]struct{ Input, Output float64 }{
	"accounts/fireworks/models/llama-v3p1-8b-instruct":                  {Input: 0.20, Output: 0.20},
	"accounts/fireworks/models/llama-v3p1-70b-instruct":                 {Input: 0.90, Output: 0.90},
	"accounts/fireworks/models/llama-v3p1-405b-instruct":                {Input: 3.00, Output: 3.00},
	"accounts/fireworks/models/llama-v3p3-70b-instruct":                 {Input: 0.90, Output: 0.90},
	"accounts/fireworks/models/llama4-scout-instruct-basic":             {Input: 0.15, Output: 0.60},
	"accounts/fireworks/models/llama4-maverick-instruct-basic":          {Input: 0.22, Output: 0.88},
	"accounts/fireworks/models/mixtral-8x7b-instruct":                   {Input: 0.50, Output: 0.50},
	"accounts/fireworks/models/mixtral-8x22b-instruct":                  {Input: 1.20, Output: 1.20},
	"accounts/fireworks/models/qwen2p5-72b-instruct":                    {Input: 0.90, Output: 0.90},
	"accounts/fireworks/models/qwen2p5-coder-32b-instruct":              {Input: 0.90, Output: 0.90},
	"accounts/fireworks/models/qwen3-30b-a3b":                           {Input: 0.22, Output: 0.88},
	"accounts/fireworks/models/qwen3-235b-a22b":                         {Input: 0.22, Output: 0.88},
	"accounts/fireworks/models/deepseek-v3":                             {Input: 0.90, Output: 0.90},
	"accounts/fireworks/models/deepseek-r1":                             {Input: 3.00, Output: 7.00},
	"accounts/fireworks/models/kimi-k2-instruct":                        {Input: 0.50, Output: 2.50},
	"accounts/fireworks/models/gemma2-9b-it":                            {Input: 0.20, Output: 0.20},
	"accounts/fireworks/models/phi-3-vision-128k-instruct":              {Input: 0.20, Output: 0.20},
}

// ModelCatalog indexes available models by ID for validation.
// It is safe for concurrent reads after creation. Dynamic catalogs
// (created via FetchModelCatalog) also support concurrent refresh.
type ModelCatalog struct {
	mu            sync.RWMutex
	models        map[string]ModelInfo
	source        string // "static" or "dynamic"
	lastRefreshed time.Time

	// Dynamic refresh fields (only populated for dynamic catalogs).
	apiKey       string
	togetherKey  string
	zaiKey       string
	fireworksKey string
	kilocodeKey  string
	groqKey      string
	client       *http.Client
	log          zerolog.Logger
	stopCh       chan struct{}
	stopOnce     sync.Once
}

// parseModelEntries converts raw model entries into a ModelInfo map.
// The source parameter identifies the aggregator (e.g. "openrouter", "bento", "together").
func parseModelEntries(entries []modelEntry, source string) map[string]ModelInfo {
	models := make(map[string]ModelInfo, len(entries))
	for _, entry := range entries {
		// Prefix IDs with "openrouter/" so naming is uniform with other
		// providers (together/, z-ai/, bedrock/) and the gateway router
		// can look up models by their qualified name.
		id := entry.ID
		if source == "openrouter" {
			id = "openrouter/" + entry.ID
		} else if source == "kilocode" {
			id = "kilo/" + entry.ID
		}
		info := ModelInfo{
			ID:            id,
			Name:          entry.Name,
			Source:        source,
			ContextLength: entry.ContextLength,
		}
		if entry.TopProvider.MaxCompletionTokens != nil {
			info.MaxOutputTokens = *entry.TopProvider.MaxCompletionTokens
		}
		if rate, err := strconv.ParseFloat(entry.Pricing.Prompt, 64); err == nil {
			info.PromptCost = rate
		}
		if rate, err := strconv.ParseFloat(entry.Pricing.Completion, 64); err == nil {
			info.CompletionCost = rate
		}
		info.Created = entry.Created
		if len(entry.Architecture.InputModalities) > 0 {
			info.InputModalities = entry.Architecture.InputModalities
		}
		if len(entry.SupportedParams) > 0 {
			info.SupportedParams = entry.SupportedParams
			for _, p := range entry.SupportedParams {
				if p == "tools" || p == "tool_choice" {
					info.SupportsTools = true
					break
				}
			}
		}
		info.Description = entry.Description
		if entry.KnowledgeCutoff != nil {
			info.KnowledgeCutoff = *entry.KnowledgeCutoff
		}
		models[id] = info
	}
	return models
}

// LoadModelCatalog reads a models.json file and returns an indexed catalog.
// Bedrock models from the curated embedded table are always merged in, so
// routing/dropdowns show Bedrock even when no dynamic fetch runs.
func LoadModelCatalog(path string) (*ModelCatalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read models file: %w", err)
	}

	var file modelsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse models file: %w", err)
	}

	models := parseModelEntries(file.Data, "static")
	if bedrock, _, err := parseBedrockCatalog(); err == nil {
		for k, v := range bedrock {
			models[k] = v
		}
	}

	return &ModelCatalog{
		models:        models,
		source:        "static",
		lastRefreshed: time.Now(),
	}, nil
}

// FetchModelCatalog creates a dynamic model catalog by fetching available
// models from the OpenRouter API. apiKey may be empty — in that case
// OpenRouter is skipped and the catalog is populated from the embedded
// Bedrock table plus whichever additional providers have keys set via
// Set*Key before/after the initial fetch. Call StartAutoRefresh to
// periodically update the catalog in the background.
func FetchModelCatalog(apiKey string, log zerolog.Logger) (*ModelCatalog, error) {
	catalog := &ModelCatalog{
		models: make(map[string]ModelInfo),
		source: "dynamic",
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
		log:    log.With().Str("component", "model-catalog").Logger(),
		stopCh: make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := catalog.fetchFromAPI(ctx); err != nil {
		return nil, fmt.Errorf("initial model catalog fetch: %w", err)
	}

	return catalog, nil
}

// fetchFromAPI calls the configured aggregator endpoints and merges results
// into the catalog's model map. OpenRouter is the primary source when its
// key is set; additional keys (Together, Z.AI, Fireworks, Kilocode, Groq)
// overlay extra models. When no OpenRouter key is configured the refresh
// still runs and populates whatever providers are available plus Bedrock.
func (c *ModelCatalog) fetchFromAPI(ctx context.Context) error {
	models := make(map[string]ModelInfo)

	if c.apiKey != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("http get: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}

		var file modelsFile
		if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		for k, v := range parseModelEntries(file.Data, "openrouter") {
			models[k] = v
		}
	}

	if c.togetherKey != "" {
		together, err := fetchTogetherModels(ctx, c.client, c.togetherKey)
		if err != nil {
			c.log.Warn().Err(err).Msg("failed to fetch Together models during catalog refresh")
		} else {
			for k, v := range together {
				models[k] = v
			}
		}
	}

	if c.zaiKey != "" {
		zai, err := fetchZAIModels(ctx, c.client, c.zaiKey)
		if err != nil {
			c.log.Warn().Err(err).Msg("failed to fetch Z.AI models during catalog refresh")
		} else {
			for k, v := range zai {
				// Check the OpenRouter-prefixed key (openrouter/z-ai/model).
				// If found, promote it: remove the openrouter/ entry and store
				// under the direct z-ai/ key so routing goes through Z.AI.
				orKey := "openrouter/" + k
				if existing, exists := models[orKey]; exists {
					existing.Source = "zai"
					existing.ID = k // z-ai/model, not openrouter/z-ai/model
					existing.SupportsTools = true
					if v.PromptCost > 0 {
						existing.PromptCost = v.PromptCost
						existing.CompletionCost = v.CompletionCost
					}
					delete(models, orKey)
					models[k] = existing
				} else if existing, exists := models[k]; exists {
					existing.Source = "zai"
					existing.SupportsTools = true
					if v.PromptCost > 0 {
						existing.PromptCost = v.PromptCost
						existing.CompletionCost = v.CompletionCost
					}
					models[k] = existing
				} else {
					models[k] = v
				}
			}
		}
	}

	if c.fireworksKey != "" {
		fw, err := fetchFireworksModels(ctx, c.client, c.fireworksKey)
		if err != nil {
			c.log.Warn().Err(err).Msg("failed to fetch Fireworks models during catalog refresh")
		} else {
			for k, v := range fw {
				models[k] = v
			}
		}
	}

	if c.kilocodeKey != "" {
		kilo, err := fetchKilocodeModels(ctx, c.client)
		if err != nil {
			c.log.Warn().Err(err).Msg("failed to fetch Kilocode models during catalog refresh")
		} else {
			for k, v := range kilo {
				models[k] = v
			}
		}
	}

	if c.groqKey != "" {
		groq, err := fetchGroqModels(ctx, c.client, c.groqKey)
		if err != nil {
			c.log.Warn().Err(err).Msg("failed to fetch Groq models during catalog refresh")
		} else {
			for k, v := range groq {
				models[k] = v
			}
		}
	}

	// Bedrock models always come from the curated embedded table — no API
	// key required, since AWS has no runtime pricing endpoint.
	if bedrock, _, err := parseBedrockCatalog(); err == nil {
		for k, v := range bedrock {
			models[k] = v
		}
	} else {
		c.log.Warn().Err(err).Msg("failed to parse embedded Bedrock catalog")
	}

	c.mu.Lock()
	c.models = models
	c.lastRefreshed = time.Now()
	c.mu.Unlock()

	c.log.Info().Int("models", len(models)).Msg("refreshed model catalog from OpenRouter API")
	return nil
}

// SetTogetherKey configures a Together AI API key so that Together models are
// included during catalog refresh. Call MergeTogetherModels after this to
// immediately populate Together models without waiting for the next refresh.
func (c *ModelCatalog) SetTogetherKey(key string) {
	c.togetherKey = key
}

// SetZAIKey configures a Z.AI API key so that Z.AI models are included
// during catalog refresh. Call MergeZAIModels after this to immediately
// populate Z.AI models without waiting for the next refresh.
func (c *ModelCatalog) SetZAIKey(key string) {
	c.zaiKey = key
}

// MergeTogetherModels fetches chat models from the Together AI API and merges
// them into the existing catalog. Models are prefixed with "together/" so the
// router directs them to the Together provider.
func (c *ModelCatalog) MergeTogetherModels(ctx context.Context) error {
	if c.togetherKey == "" {
		return nil
	}
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	together, err := fetchTogetherModels(ctx, client, c.togetherKey)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for k, v := range together {
		c.models[k] = v
	}
	c.mu.Unlock()
	return nil
}

// MergeZAIModels fetches models from the Z.AI API and merges them into the
// existing catalog. For models already present (e.g. from OpenRouter), the
// source is updated to "zai" to indicate direct availability and z-ai pricing
// is applied. New models not yet in the catalog are added.
func (c *ModelCatalog) MergeZAIModels(ctx context.Context) error {
	if c.zaiKey == "" {
		return nil
	}
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	zai, err := fetchZAIModels(ctx, client, c.zaiKey)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for k, v := range zai {
		// Promote from openrouter/ to direct z-ai/ key when Z.AI is available.
		orKey := "openrouter/" + k
		if existing, exists := c.models[orKey]; exists {
			existing.Source = "zai"
			existing.ID = k
			existing.SupportsTools = true
			if v.PromptCost > 0 {
				existing.PromptCost = v.PromptCost
				existing.CompletionCost = v.CompletionCost
			}
			delete(c.models, orKey)
			c.models[k] = existing
		} else if existing, exists := c.models[k]; exists {
			existing.Source = "zai"
			existing.SupportsTools = true
			if v.PromptCost > 0 {
				existing.PromptCost = v.PromptCost
				existing.CompletionCost = v.CompletionCost
			}
			c.models[k] = existing
		} else {
			c.models[k] = v
		}
	}
	c.mu.Unlock()
	return nil
}

// SetFireworksKey configures a Fireworks AI API key so that Fireworks models
// are included during catalog refresh.
func (c *ModelCatalog) SetFireworksKey(key string) {
	c.fireworksKey = key
}

// SetKilocodeKey configures a Kilocode API key so that Kilo Gateway models
// are included during catalog refresh. The Kilo /models endpoint itself is
// public; the key is used as a gate so Kilo models only appear when the
// operator has actually configured the provider.
func (c *ModelCatalog) SetKilocodeKey(key string) {
	c.kilocodeKey = key
}

// MergeKilocodeModels fetches models from the Kilo Gateway /models endpoint
// and merges them into the existing catalog, prefixed with "kilo/" so the
// router directs them to the Kilocode provider.
func (c *ModelCatalog) MergeKilocodeModels(ctx context.Context) error {
	if c.kilocodeKey == "" {
		return nil
	}
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	kilo, err := fetchKilocodeModels(ctx, client)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for k, v := range kilo {
		c.models[k] = v
	}
	c.mu.Unlock()
	return nil
}

// groqModelEntry matches the Groq /openai/v1/models JSON shape.
type groqModelEntry struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Created       int64  `json:"created"`
	OwnedBy       string `json:"owned_by"`
	Active        bool   `json:"active"`
	ContextWindow int    `json:"context_window"`
}

type groqModelsResponse struct {
	Object string           `json:"object"`
	Data   []groqModelEntry `json:"data"`
}

// SetGroqKey configures a Groq API key so that Groq models are included
// during catalog refresh.
func (c *ModelCatalog) SetGroqKey(key string) {
	c.groqKey = key
}

// MergeGroqModels fetches models from the Groq /openai/v1/models endpoint
// and merges them into the existing catalog, prefixed with "groq/" so the
// router directs them to the Groq provider.
func (c *ModelCatalog) MergeGroqModels(ctx context.Context) error {
	if c.groqKey == "" {
		return nil
	}
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	groq, err := fetchGroqModels(ctx, client, c.groqKey)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for k, v := range groq {
		c.models[k] = v
	}
	c.mu.Unlock()
	return nil
}

// fetchGroqModels calls the Groq /openai/v1/models endpoint and returns
// models as a ModelInfo map keyed by "groq/<model-id>". Only active models
// are included. Groq does not return pricing in the /models response, so
// PromptCost and CompletionCost stay zero — real costs must come from an
// external pricing source (e.g. the LiteLLM pricing registry).
func fetchGroqModels(ctx context.Context, client *http.Client, apiKey string) (map[string]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.groq.com/openai/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build groq request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("groq http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("groq unexpected status %d", resp.StatusCode)
	}
	var body groqModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode groq response: %w", err)
	}
	models := make(map[string]ModelInfo)
	for _, entry := range body.Data {
		if !entry.Active {
			continue
		}
		id := "groq/" + entry.ID
		models[id] = ModelInfo{
			ID:            id,
			Name:          entry.ID,
			Source:        "groq",
			ContextLength: entry.ContextWindow,
			Created:       entry.Created,
			SupportsTools: true,
		}
	}
	return models, nil
}

// fetchKilocodeModels calls the Kilo Gateway /models endpoint and returns
// models as a ModelInfo map keyed by "kilo/<provider>/<model>". The endpoint
// is public (no auth required) and returns the OpenRouter-compatible schema
// including pricing, context length, and supported parameters, so no
// hardcoded tables are required.
func fetchKilocodeModels(ctx context.Context, client *http.Client) (map[string]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.kilo.ai/api/gateway/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build kilocode request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kilocode http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kilocode unexpected status %d", resp.StatusCode)
	}
	var file modelsFile
	if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
		return nil, fmt.Errorf("decode kilocode response: %w", err)
	}
	return parseModelEntries(file.Data, "kilocode"), nil
}

// MergeFireworksModels fetches models from the Fireworks AI API and merges
// them into the existing catalog. Models are prefixed with "fireworks/" so the
// router directs them to the Fireworks provider.
func (c *ModelCatalog) MergeFireworksModels(ctx context.Context) error {
	if c.fireworksKey == "" {
		return nil
	}
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	fw, err := fetchFireworksModels(ctx, client, c.fireworksKey)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for k, v := range fw {
		c.models[k] = v
	}
	c.mu.Unlock()
	return nil
}

// fetchFireworksModels calls the Fireworks AI /v1/models endpoint and returns
// models as a ModelInfo map keyed by "fireworks/<model-id>".
func fetchFireworksModels(ctx context.Context, client *http.Client, apiKey string) (map[string]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.fireworks.ai/inference/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build fireworks request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fireworks http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fireworks unexpected status %d", resp.StatusCode)
	}

	var body fireworksModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode fireworks response: %w", err)
	}

	models := make(map[string]ModelInfo)
	for _, entry := range body.Data {
		id := "fireworks/" + entry.ID
		info := ModelInfo{
			ID:            id,
			Name:          entry.ID,
			Source:        "fireworks",
			ContextLength: 131072, // common default; most Fireworks models support 128K
			SupportsTools: true,
		}
		if pricing, ok := fireworksDefaultPricing[entry.ID]; ok {
			info.PromptCost = pricing.Input / 1_000_000
			info.CompletionCost = pricing.Output / 1_000_000
		}
		models[id] = info
	}
	return models, nil
}

// MergeBedrockModels injects the curated Bedrock models from the embedded
// bedrock_models.json into the live catalog. Idempotent — re-calls replace
// existing bedrock/* entries. Unlike the Together/Z.AI mergers, this one
// needs no API key: AWS Bedrock has no runtime pricing endpoint so the data
// is shipped in-binary.
func (c *ModelCatalog) MergeBedrockModels() error {
	bedrock, _, err := parseBedrockCatalog()
	if err != nil {
		return err
	}
	c.mu.Lock()
	for k, v := range bedrock {
		c.models[k] = v
	}
	c.mu.Unlock()
	return nil
}

// fetchZAIModels calls the Z.AI /api/paas/v4/models endpoint and returns
// models as a ModelInfo map keyed by "z-ai/<model-id>".
func fetchZAIModels(ctx context.Context, client *http.Client, apiKey string) (map[string]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.z.ai/api/paas/v4/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build zai request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zai http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zai unexpected status %d", resp.StatusCode)
	}

	var body zaiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode zai response: %w", err)
	}

	models := make(map[string]ModelInfo)
	for _, entry := range body.Data {
		id := "z-ai/" + entry.ID
		info := ModelInfo{
			ID:            id,
			Name:          entry.ID,
			Source:        "zai",
			ContextLength: zaiDefaultContextLength,
			Created:       entry.Created,
			SupportsTools: true,
		}
		if pricing, ok := zaiDefaultPricing[entry.ID]; ok {
			info.PromptCost = pricing.Input / 1_000_000
			info.CompletionCost = pricing.Output / 1_000_000
		}
		models[id] = info
	}
	return models, nil
}

// fetchTogetherModels calls the Together AI /v1/models endpoint and returns
// chat models as a ModelInfo map keyed by "together/<model-id>".
func fetchTogetherModels(ctx context.Context, client *http.Client, apiKey string) (map[string]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.together.xyz/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build together request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("together http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("together unexpected status %d", resp.StatusCode)
	}

	var entries []togetherModelEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode together response: %w", err)
	}

	models := make(map[string]ModelInfo)
	for _, entry := range entries {
		if entry.Type != "chat" {
			continue
		}
		id := "together/" + entry.ID
		models[id] = ModelInfo{
			ID:             id,
			Name:           entry.DisplayName,
			Source:         "together",
			ContextLength:  entry.ContextLength,
			PromptCost:     entry.Pricing.Input / 1_000_000,
			CompletionCost: entry.Pricing.Output / 1_000_000,
			Created:        entry.Created,
		}
	}
	return models, nil
}

// StartAutoRefresh begins a background goroutine that refreshes the catalog
// every 6 hours. Only works for dynamic catalogs (created via FetchModelCatalog).
func (c *ModelCatalog) StartAutoRefresh() {
	if c.stopCh == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-c.stopCh:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := c.fetchFromAPI(ctx); err != nil {
					c.log.Warn().Err(err).Msg("failed to refresh model catalog")
				}
				cancel()
			}
		}
	}()
}

// Refresh manually triggers a catalog refresh by re-fetching from every
// configured aggregator. Returns an error for static catalogs (loaded from
// a file), which have no keys and cannot refresh.
func (c *ModelCatalog) Refresh() error {
	if c.source != "dynamic" {
		return fmt.Errorf("cannot refresh static model catalog")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.fetchFromAPI(ctx)
}

// Stop signals the background auto-refresh goroutine to exit.
// Safe to call multiple times.
func (c *ModelCatalog) Stop() {
	c.stopOnce.Do(func() {
		if c.stopCh != nil {
			close(c.stopCh)
		}
	})
}

// Source returns "static" or "dynamic" indicating how the catalog was loaded.
func (c *ModelCatalog) Source() string {
	return c.source
}

// LastRefreshed returns when the catalog data was last loaded or refreshed.
func (c *ModelCatalog) LastRefreshed() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastRefreshed
}

// Validate checks whether a model ID exists in the catalog. It also tries
// an "openrouter/"-prefixed lookup for backward compatibility.
func (c *ModelCatalog) Validate(model string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.models[model]; ok {
		return nil
	}
	if strings.Contains(model, "/") && !strings.HasPrefix(model, "openrouter/") {
		if _, ok := c.models["openrouter/"+model]; ok {
			return nil
		}
	}
	return fmt.Errorf("unknown model %q: not found in model catalog", model)
}

// Get returns the ModelInfo for a given model ID. If the exact ID is not
// found, it also tries an "openrouter/"-prefixed lookup since OpenRouter
// models are stored with that prefix for uniform naming.
func (c *ModelCatalog) Get(model string) (ModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.models[model]
	if !ok && strings.Contains(model, "/") && !strings.HasPrefix(model, "openrouter/") {
		info, ok = c.models["openrouter/"+model]
	}
	return info, ok
}

// SupportsVision returns true if the model supports image input.
func (c *ModelCatalog) SupportsVision(modelID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.models[modelID]
	if !ok && strings.Contains(modelID, "/") && !strings.HasPrefix(modelID, "openrouter/") {
		info, ok = c.models["openrouter/"+modelID]
	}
	if !ok {
		return false
	}
	for _, m := range info.InputModalities {
		if m == "image" {
			return true
		}
	}
	return false
}

// Len returns the number of models in the catalog.
func (c *ModelCatalog) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.models)
}

// List returns all models in the catalog sorted by ID.
func (c *ModelCatalog) List() []ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	models := make([]ModelInfo, 0, len(c.models))
	for _, m := range c.models {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Created != models[j].Created {
			return models[i].Created > models[j].Created
		}
		return models[i].ID < models[j].ID
	})
	return models
}
