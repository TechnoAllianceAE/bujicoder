package llm

// Static cost registry — hardcoded pricing for major models across all providers.
// Prices are in USD per token (matching OpenRouter format).
// This provides O(1) lookups with zero network dependency.
//
// Source: Official provider pricing pages as of 2026-03.
// These are baseline prices; the PricingService will overlay fresh API-fetched
// prices on top, so stale entries here won't cause incorrect billing — they
// only serve as fallback when the API is unavailable.

// costEntry holds per-million-token pricing (easier to read and maintain).
type costEntry struct {
	Input  float64 // USD per 1M input tokens
	Output float64 // USD per 1M output tokens
}

// staticCostRegistry maps model IDs (OpenRouter-style "provider/model") to pricing.
// Populated at init from the per-provider tables below.
var staticCostRegistry map[string]ModelPricing

func init() {
	staticCostRegistry = make(map[string]ModelPricing, len(anthropicModels)+len(openaiModels)+len(googleModels)+len(xaiModels)+len(metaModels)+len(deepseekModels)+len(mistralModels)+len(qwenModels))

	register := func(models map[string]costEntry) {
		for id, c := range models {
			staticCostRegistry[id] = ModelPricing{
				PromptCostPerToken:     c.Input / 1_000_000,
				CompletionCostPerToken: c.Output / 1_000_000,
			}
		}
	}

	register(anthropicModels)
	register(openaiModels)
	register(googleModels)
	register(xaiModels)
	register(metaModels)
	register(deepseekModels)
	register(mistralModels)
	register(qwenModels)
}

// GetStaticPricing returns the static cost registry. The returned map must not
// be modified by callers.
func GetStaticPricing() map[string]ModelPricing {
	return staticCostRegistry
}

// --- Anthropic ---
var anthropicModels = map[string]costEntry{
	// Claude 4.6
	"anthropic/claude-opus-4-6-20250626":  {Input: 15.00, Output: 75.00},
	"anthropic/claude-sonnet-4-6-20250514": {Input: 3.00, Output: 15.00},
	// Claude 4.5
	"anthropic/claude-haiku-4-5-20251001": {Input: 0.80, Output: 4.00},
	// Claude 4
	"anthropic/claude-sonnet-4-20250514": {Input: 3.00, Output: 15.00},
	// Claude 3.5
	"anthropic/claude-3.5-sonnet":       {Input: 3.00, Output: 15.00},
	"anthropic/claude-3.5-sonnet-20241022": {Input: 3.00, Output: 15.00},
	"anthropic/claude-3.5-haiku":        {Input: 0.80, Output: 4.00},
	"anthropic/claude-3.5-haiku-20241022": {Input: 0.80, Output: 4.00},
	// Claude 3
	"anthropic/claude-3-opus":           {Input: 15.00, Output: 75.00},
	"anthropic/claude-3-sonnet":         {Input: 3.00, Output: 15.00},
	"anthropic/claude-3-haiku":          {Input: 0.25, Output: 1.25},
}

// --- OpenAI ---
var openaiModels = map[string]costEntry{
	// GPT-4.1
	"openai/gpt-4.1":             {Input: 2.00, Output: 8.00},
	"openai/gpt-4.1-mini":        {Input: 0.40, Output: 1.60},
	"openai/gpt-4.1-nano":        {Input: 0.10, Output: 0.40},
	// GPT-4o
	"openai/gpt-4o":              {Input: 2.50, Output: 10.00},
	"openai/gpt-4o-2024-11-20":   {Input: 2.50, Output: 10.00},
	"openai/gpt-4o-mini":         {Input: 0.15, Output: 0.60},
	"openai/gpt-4o-mini-2024-07-18": {Input: 0.15, Output: 0.60},
	// o-series reasoning
	"openai/o3":                   {Input: 10.00, Output: 40.00},
	"openai/o3-mini":              {Input: 1.10, Output: 4.40},
	"openai/o4-mini":              {Input: 1.10, Output: 4.40},
	"openai/o1":                   {Input: 15.00, Output: 60.00},
	"openai/o1-mini":              {Input: 3.00, Output: 12.00},
	"openai/o1-preview":           {Input: 15.00, Output: 60.00},
	// GPT-4 Turbo / GPT-4
	"openai/gpt-4-turbo":         {Input: 10.00, Output: 30.00},
	"openai/gpt-4":               {Input: 30.00, Output: 60.00},
}

// --- Google ---
var googleModels = map[string]costEntry{
	// Gemini 2.5
	"google/gemini-2.5-pro-preview":  {Input: 1.25, Output: 10.00},
	"google/gemini-2.5-flash-preview": {Input: 0.15, Output: 0.60},
	"google/gemini-2.5-flash":         {Input: 0.15, Output: 0.60},
	// Gemini 2.0
	"google/gemini-2.0-flash":     {Input: 0.10, Output: 0.40},
	"google/gemini-2.0-flash-001": {Input: 0.10, Output: 0.40},
	// Gemini 1.5
	"google/gemini-1.5-pro":       {Input: 1.25, Output: 5.00},
	"google/gemini-1.5-flash":     {Input: 0.075, Output: 0.30},
	// Gemini Pro
	"google/gemini-pro":           {Input: 0.125, Output: 0.375},
}

// --- xAI ---
var xaiModels = map[string]costEntry{
	"x-ai/grok-3":              {Input: 3.00, Output: 15.00},
	"x-ai/grok-3-mini":         {Input: 0.30, Output: 0.50},
	"x-ai/grok-3-fast":         {Input: 5.00, Output: 25.00},
	"x-ai/grok-2":              {Input: 2.00, Output: 10.00},
	"x-ai/grok-2-mini":         {Input: 0.20, Output: 1.00},
	"x-ai/grok-beta":           {Input: 5.00, Output: 15.00},
}

// --- Meta (via OpenRouter / Together / Groq) ---
var metaModels = map[string]costEntry{
	"meta-llama/llama-4-maverick":  {Input: 0.50, Output: 0.70},
	"meta-llama/llama-4-scout":     {Input: 0.18, Output: 0.35},
	"meta-llama/llama-3.3-70b-instruct": {Input: 0.39, Output: 0.39},
	"meta-llama/llama-3.1-405b-instruct": {Input: 2.00, Output: 2.00},
	"meta-llama/llama-3.1-70b-instruct": {Input: 0.39, Output: 0.39},
	"meta-llama/llama-3.1-8b-instruct":  {Input: 0.055, Output: 0.055},
}

// --- DeepSeek ---
var deepseekModels = map[string]costEntry{
	"deepseek/deepseek-chat":     {Input: 0.27, Output: 1.10},
	"deepseek/deepseek-r1":       {Input: 0.55, Output: 2.19},
	"deepseek/deepseek-v3":       {Input: 0.27, Output: 1.10},
	"deepseek/deepseek-r1-0528":  {Input: 0.55, Output: 2.19},
}

// --- Mistral ---
var mistralModels = map[string]costEntry{
	"mistralai/mistral-large":     {Input: 2.00, Output: 6.00},
	"mistralai/mistral-medium":    {Input: 2.70, Output: 8.10},
	"mistralai/mistral-small":     {Input: 0.20, Output: 0.60},
	"mistralai/mistral-nemo":      {Input: 0.13, Output: 0.13},
	"mistralai/codestral":         {Input: 0.30, Output: 0.90},
	"mistralai/mixtral-8x7b":      {Input: 0.24, Output: 0.24},
}

// --- Qwen ---
var qwenModels = map[string]costEntry{
	"qwen/qwen-3-235b":          {Input: 0.80, Output: 2.40},
	"qwen/qwen-3-32b":           {Input: 0.20, Output: 0.60},
	"qwen/qwen-3-30b-a3b":       {Input: 0.13, Output: 0.13},
	"qwen/qwen-2.5-72b-instruct": {Input: 0.36, Output: 0.36},
	"qwen/qwen-2.5-coder-32b-instruct": {Input: 0.20, Output: 0.20},
	"qwen/qwq-32b":              {Input: 0.20, Output: 0.60},
}
