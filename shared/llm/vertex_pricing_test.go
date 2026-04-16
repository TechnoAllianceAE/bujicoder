package llm

import (
	"math"
	"strings"
	"testing"
)

func TestParseVertexPricingPage(t *testing.T) {
	html := `
<html><body>
  <div>Claude Sonnet 4.6 Input: $3.30 Output: $16.50</div>
  <div>Zero Model Input: $0.00 Output: $9.00</div>
  <h2>Gemini 2.0 Flash</h2>
  <p>Input price is $0.10 and Output price is $0.40 per 1M tokens</p>
  <h2>Gemini 1.5 Pro</h2>
  <p>Input price is $1.25 and Output price is $5.00 per 1M tokens</p>
</body></html>`

	pricing := parseVertexPricingPage([]byte(html))

	if _, ok := pricing["zero model"]; ok {
		t.Fatal("expected zero-priced model to be skipped")
	}

	claude, ok := pricing["claude sonnet 4 6"]
	if !ok {
		t.Fatal("expected Claude pricing to be parsed")
	}
	if claude.PromptCostPerToken <= 0 || claude.CompletionCostPerToken <= 0 {
		t.Fatal("expected Claude pricing values to be positive")
	}

	gemini, ok := pricing["gemini 2 0 flash"]
	if !ok {
		t.Fatal("expected Gemini 2.0 Flash pricing to be parsed")
	}
	if math.Abs(gemini.PromptCostPerToken-(0.10/1_000_000)) > 1e-12 {
		t.Fatalf("gemini prompt = %f", gemini.PromptCostPerToken)
	}
	if math.Abs(gemini.CompletionCostPerToken-(0.40/1_000_000)) > 1e-12 {
		t.Fatalf("gemini completion = %f", gemini.CompletionCostPerToken)
	}
}

func TestExtractHTMLTextSkipsScriptStyle(t *testing.T) {
	html := `<html><body>
<style>.x{content:"Input: $999 Output: $999"}</style>
<script>var x = "Input: $888 Output: $888";</script>
<div>Visible Input: $1 Output: $2</div>
</body></html>`

	text := extractHTMLText([]byte(html))
	if strings.Contains(text, "$999") || strings.Contains(text, "$888") {
		t.Fatalf("expected script/style text to be excluded, got: %s", text)
	}
}
