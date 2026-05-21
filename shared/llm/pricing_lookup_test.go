package llm

import (
	"testing"

	"github.com/rs/zerolog"
)

// TestPricingLookupQualifiedNames verifies the price map (keyed by bare vendor
// id) resolves for provider-qualified routed model names. Regression for the
// cost=0 incident where "openrouter/z-ai/glm-5.1" missed the "z-ai/glm-5.1" key.
func TestPricingLookupQualifiedNames(t *testing.T) {
	p := NewPricingService("", zerolog.Nop())
	p.prices = map[string]ModelPricing{
		"z-ai/glm-5.1":            {PromptCostPerToken: 0.000001, CompletionCostPerToken: 0.000003},
		"minimax/minimax-m2.7":    {PromptCostPerToken: 0.000002, CompletionCostPerToken: 0.000002},
		"anthropic/claude-opus-4": {PromptCostPerToken: 0.00001, CompletionCostPerToken: 0.00003},
	}

	tests := []struct {
		model    string
		wantOK   bool
		wantKey  string // expected resolved key (for sanity)
	}{
		{"z-ai/glm-5.1", true, "z-ai/glm-5.1"},
		{"openrouter/z-ai/glm-5.1", true, "z-ai/glm-5.1"},
		{"kilocode/z-ai/glm-5.1", true, "z-ai/glm-5.1"},
		{"openrouter/minimax/minimax-m2.7", true, "minimax/minimax-m2.7"},
		{"openrouter/anthropic/claude-opus-4", true, "anthropic/claude-opus-4"},
		{"ollama/glm-5.1:cloud", false, ""},
		{"unknown/model/xyz", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := p.lookup(tt.model)
			if ok != tt.wantOK {
				t.Fatalf("lookup(%q) ok=%v, want %v", tt.model, ok, tt.wantOK)
			}
			if ok {
				want := p.prices[tt.wantKey]
				if got != want {
					t.Errorf("lookup(%q) = %+v, want %+v (key %q)", tt.model, got, want, tt.wantKey)
				}
			}
		})
	}

	// End-to-end: cost must be nonzero for the qualified name.
	if c := p.CalculateCostCents("openrouter/z-ai/glm-5.1", 1_000_000, 1_000_000); c <= 0 {
		t.Errorf("CalculateCostCents qualified = %d, want > 0", c)
	}
}
