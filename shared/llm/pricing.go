package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ModelPricing holds per-token costs in USD for a single model.
type ModelPricing struct {
	PromptCostPerToken     float64 // USD per token
	CompletionCostPerToken float64 // USD per token
}

// PricingService fetches model pricing from the OpenRouter API, caches it
// in-memory, and calculates per-request costs in cents.
type PricingService struct {
	mu     sync.RWMutex
	prices map[string]ModelPricing // key: model ID e.g. "openai/gpt-4"

	apiKey      string
	togetherKey string
	zaiKey      string
	client      *http.Client
	log         zerolog.Logger
	stopCh      chan struct{}
}

// NewPricingService creates a new pricing service. Call Start to fetch
// initial pricing and begin periodic refresh.
func NewPricingService(apiKey string, log zerolog.Logger) *PricingService {
	return &PricingService{
		prices: make(map[string]ModelPricing),
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
		log:    log.With().Str("component", "pricing").Logger(),
		stopCh: make(chan struct{}),
	}
}

// Start fetches pricing immediately and starts a background goroutine that
// refreshes every 6 hours. Returns an error only if the initial fetch fails.
func (p *PricingService) Start(ctx context.Context) error {
	if err := p.fetchPricing(ctx); err != nil {
		return fmt.Errorf("initial pricing fetch: %w", err)
	}

	go p.refreshLoop()
	return nil
}

// Stop signals the background refresh goroutine to exit.
func (p *PricingService) Stop() {
	close(p.stopCh)
}

// CalculateCostCents computes the cost in cents for a given model and token
// counts. Returns 0 for unknown models (logged at debug level).
func (p *PricingService) CalculateCostCents(model string, inputTokens, outputTokens int) int64 {
	p.mu.RLock()
	pricing, ok := p.prices[model]
	p.mu.RUnlock()

	if !ok {
		p.log.Debug().Str("model", model).Msg("no pricing for model")
		return 0
	}

	costUSD := float64(inputTokens)*pricing.PromptCostPerToken +
		float64(outputTokens)*pricing.CompletionCostPerToken
	costCents := int64(math.Ceil(costUSD * 100))
	return costCents
}

// refreshLoop periodically re-fetches pricing data until Stop is called.
func (p *PricingService) refreshLoop() {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := p.fetchPricing(ctx); err != nil {
				p.log.Warn().Err(err).Msg("failed to refresh pricing")
			}
			cancel()
		}
	}
}

// openRouterModelResponse matches the OpenRouter /api/v1/models JSON shape.
type openRouterModelResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Pricing struct {
			Prompt     string `json:"prompt"`     // USD per token as string
			Completion string `json:"completion"` // USD per token as string
		} `json:"pricing"`
	} `json:"data"`
}

// SetTogetherKey configures a Together AI API key so that Together model
// pricing is included during refresh.
func (p *PricingService) SetTogetherKey(key string) {
	p.togetherKey = key
}

// SetZAIKey configures a Z.AI API key so that Z.AI model pricing is
// included during refresh.
func (p *PricingService) SetZAIKey(key string) {
	p.zaiKey = key
}

// fetchPricing calls the OpenRouter models API (and optionally the Together AI
// models API) and populates the price map.
func (p *PricingService) fetchPricing(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var body openRouterModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	prices := make(map[string]ModelPricing, len(body.Data))
	var skipped int
	for _, m := range body.Data {
		promptRate, err1 := strconv.ParseFloat(m.Pricing.Prompt, 64)
		completionRate, err2 := strconv.ParseFloat(m.Pricing.Completion, 64)
		if err1 != nil || err2 != nil {
			skipped++
			continue
		}
		prices[m.ID] = ModelPricing{
			PromptCostPerToken:     promptRate,
			CompletionCostPerToken: completionRate,
		}
	}

	if p.togetherKey != "" {
		if err := p.mergeTogetherPricing(ctx, prices); err != nil {
			p.log.Warn().Err(err).Msg("failed to fetch Together pricing")
		}
	}

	if p.zaiKey != "" {
		p.mergeZAIPricing(prices)
	}

	p.mu.Lock()
	p.prices = prices
	p.mu.Unlock()

	p.log.Info().Int("models", len(prices)).Int("skipped", skipped).Msg("loaded model prices")
	return nil
}

// mergeZAIPricing adds fallback Z.AI pricing for models not already present
// in the price map (e.g. from OpenRouter). OpenRouter pricing is authoritative;
// the hardcoded rates are only used for models Z.AI offers but OpenRouter
// hasn't listed yet.
func (p *PricingService) mergeZAIPricing(prices map[string]ModelPricing) {
	for modelID, rate := range zaiDefaultPricing {
		id := "z-ai/" + modelID
		if _, exists := prices[id]; !exists {
			prices[id] = ModelPricing{
				PromptCostPerToken:     rate.Input / 1_000_000,
				CompletionCostPerToken: rate.Output / 1_000_000,
			}
		}
	}
}

// mergeTogetherPricing fetches pricing from the Together AI API and adds it
// to the given price map. Together prices are in USD per million tokens;
// they are converted to per-token to match the OpenRouter format.
func (p *PricingService) mergeTogetherPricing(ctx context.Context, prices map[string]ModelPricing) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.together.xyz/v1/models", nil)
	if err != nil {
		return fmt.Errorf("build together request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.togetherKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("together http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("together unexpected status %d", resp.StatusCode)
	}

	var entries []togetherModelEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return fmt.Errorf("decode together response: %w", err)
	}

	for _, entry := range entries {
		if entry.Type != "chat" {
			continue
		}
		id := "together/" + entry.ID
		prices[id] = ModelPricing{
			PromptCostPerToken:     entry.Pricing.Input / 1_000_000,
			CompletionCostPerToken: entry.Pricing.Output / 1_000_000,
		}
	}
	return nil
}
