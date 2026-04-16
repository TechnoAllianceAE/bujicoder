package llm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

const vertexPricingURL = "https://cloud.google.com/vertex-ai/generative-ai/pricing"

var vertexInlinePricingPattern = regexp.MustCompile(`(?i)^(.+?) Input:\s*\$([0-9.]+).+?Output:\s*\$([0-9.]+)`)

type vertexPricingPatternSpec struct {
	anchor  string
	pattern *regexp.Regexp
}

var googleVertexPricingPatterns = map[string]vertexPricingPatternSpec{
	"gemini 2 5 pro": {
		anchor:  "Gemini 2.5 Pro",
		pattern: regexp.MustCompile(`(?is)Input[^$]*\$([0-9.]+).*?Text output[^$]*\$([0-9.]+)`),
	},
	"gemini 2 5 flash": {
		anchor:  "Gemini 2.5 Flash",
		pattern: regexp.MustCompile(`(?is)Input[^$]*\$([0-9.]+).*?Text output[^$]*\$([0-9.]+)`),
	},
	"gemini 2 0 flash": {
		anchor:  "Gemini 2.0 Flash",
		pattern: regexp.MustCompile(`(?is)Input[^$]*\$([0-9.]+).*?Output[^$]*\$([0-9.]+)`),
	},
	"gemini 1 5 pro": {
		anchor:  "Gemini 1.5 Pro",
		pattern: regexp.MustCompile(`(?is)Input[^$]*\$([0-9.]+).*?Output[^$]*\$([0-9.]+)`),
	},
	"gemini 1 5 flash": {
		anchor:  "Gemini 1.5 Flash",
		pattern: regexp.MustCompile(`(?is)Input[^$]*\$([0-9.]+).*?Output[^$]*\$([0-9.]+)`),
	},
}

func (p *PricingService) mergeVertexPricing(ctx context.Context, prices map[string]ModelPricing) error {
	models, err := p.vertex.ListModels(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vertexPricingURL, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vertex pricing status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	entries := parseVertexPricingPage(body)
	if len(entries) == 0 {
		p.log.Warn().Msg("vertex pricing scrape returned zero parsed entries")
	}
	var applied int
	for _, model := range models {
		if pricing, ok := entries[vertexPricingLookupKey(model.ID)]; ok {
			prices[model.ID] = pricing
			applied++
			continue
		}
		// Google Gemini on Vertex maps cleanly to existing google/* pricing IDs.
		if strings.HasPrefix(model.ID, "vertex/") && !strings.Contains(strings.TrimPrefix(model.ID, "vertex/"), "/") {
			if googlePricing, ok := prices["google/"+strings.TrimPrefix(model.ID, "vertex/")]; ok {
				prices[model.ID] = googlePricing
				applied++
			}
		}
	}
	if applied == 0 {
		p.log.Warn().Int("catalog_models", len(models)).Msg("vertex pricing merge applied zero model prices")
	}
	return nil
}

func parseVertexPricingPage(body []byte) map[string]ModelPricing {
	text := extractHTMLText(body)
	lines := strings.Split(text, "\n")
	result := map[string]ModelPricing{}
	for _, raw := range lines {
		line := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
		if line == "" {
			continue
		}
		matches := vertexInlinePricingPattern.FindStringSubmatch(line)
		if len(matches) != 4 {
			continue
		}
		input, err1 := strconv.ParseFloat(matches[2], 64)
		output, err2 := strconv.ParseFloat(matches[3], 64)
		if err1 != nil || err2 != nil || input <= 0 || output <= 0 {
			continue
		}
		result[normalizeVertexPricingName(matches[1])] = ModelPricing{
			PromptCostPerToken:     input / 1_000_000,
			CompletionCostPerToken: output / 1_000_000,
		}
	}
	for key, spec := range googleVertexPricingPatterns {
		window := vertexPricingWindow(text, spec.anchor, 2200)
		if window == "" {
			continue
		}
		matches := spec.pattern.FindStringSubmatch(window)
		if len(matches) != 3 {
			continue
		}
		input, err1 := strconv.ParseFloat(matches[1], 64)
		output, err2 := strconv.ParseFloat(matches[2], 64)
		if err1 != nil || err2 != nil || input <= 0 || output <= 0 {
			continue
		}
		result[key] = ModelPricing{
			PromptCostPerToken:     input / 1_000_000,
			CompletionCostPerToken: output / 1_000_000,
		}
	}
	return result
}

func extractHTMLText(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return string(body)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				b.WriteString(text)
				b.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}

func vertexPricingWindow(text, anchor string, span int) string {
	i := strings.Index(text, anchor)
	if i < 0 {
		return ""
	}
	end := i + span
	if end > len(text) {
		end = len(text)
	}
	return text[i:end]
}

func vertexPricingLookupKey(modelID string) string {
	switch {
	case strings.HasPrefix(modelID, "vertex/anthropic/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/anthropic/"))
	case strings.HasPrefix(modelID, "vertex/meta/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/meta/"))
	case strings.HasPrefix(modelID, "vertex/mistralai/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/mistralai/"))
	case strings.HasPrefix(modelID, "vertex/openai/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/openai/"))
	case strings.HasPrefix(modelID, "vertex/x-ai/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/x-ai/"))
	case strings.HasPrefix(modelID, "vertex/deepseek/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/deepseek/"))
	case strings.HasPrefix(modelID, "vertex/qwen/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/qwen/"))
	case strings.HasPrefix(modelID, "vertex/minimax/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/minimax/"))
	case strings.HasPrefix(modelID, "vertex/moonshotai/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/moonshotai/"))
	case strings.HasPrefix(modelID, "vertex/z-ai/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/z-ai/"))
	case strings.HasPrefix(modelID, "vertex/ai21/"):
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/ai21/"))
	default:
		return normalizeVertexPricingName(strings.TrimPrefix(modelID, "vertex/"))
	}
}

func normalizeVertexPricingName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "’", "")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.Join(strings.Fields(s), " ")

	replacements := map[string]string{
		"claude sonnet 4 6": "claude sonnet 4 6",
		"claude sonnet 4 5": "claude sonnet 4 5",
		"claude haiku 4 5":  "claude haiku 4 5",
		"claude opus 4 5":   "claude opus 4 5",
		"claude opus 4 1":   "claude opus 4 1",
		"llama 4 scout":     "llama 4 scout",
		"llama 4 maverick":  "llama 4 maverick",
		"mistral medium 3":  "mistral medium 3",
		"mistral small 3 1": "mistral small 3 1",
		"codestral 2":       "codestral 2",
		"grok 4 20":         "grok 4 20 non reasoning",
	}
	if repl, ok := replacements[s]; ok {
		return repl
	}
	return s
}
