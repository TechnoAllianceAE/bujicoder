package agentruntime

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// Compaction thresholds — trigger compaction when input tokens exceed this
// fraction of the model's context window.
const (
	CompactTriggerRatio = 0.70 // 70% of context window
	CompactTargetRatio  = 0.40 // compress down to ~40%

	// Fallback context window size when the model's actual window is unknown.
	DefaultContextWindow = 128000

	// Number of recent messages to never compact (keep verbatim).
	CompactKeepRecent = 8

	// Model context window lookup (common models).
	// The runtime will use this as a fallback if the provider doesn't report it.
	compactSummaryMaxTokens = 2048
)

// contextWindowEntry maps a model prefix to its context window size.
// Ordered by specificity (longest prefix first) to avoid ambiguous matches.
type contextWindowEntry struct {
	prefix string
	window int
}

var knownContextWindows = []contextWindowEntry{
	{"gpt-4o", 128000},   // must come before "gpt-4"
	{"gpt-4", 128000},
	{"claude", 200000},
	{"gemini", 1000000},
	{"deepseek", 128000},
	{"qwen", 128000},
	{"minimax", 1000000},
	{"llama", 128000},
	{"mistral", 128000},
}

// estimateContextWindow returns the context window for a model.
func estimateContextWindow(model string) int {
	lower := strings.ToLower(model)
	for _, entry := range knownContextWindows {
		if strings.Contains(lower, entry.prefix) {
			return entry.window
		}
	}
	return DefaultContextWindow
}

// shouldCompact returns true if the conversation needs compaction based on
// the last step's input token count (which reflects actual context size)
// and the model's context window.
func shouldCompact(lastStepInputTokens int, model string) bool {
	window := estimateContextWindow(model)
	threshold := int(float64(window) * CompactTriggerRatio)
	return lastStepInputTokens > threshold
}

// compactUsage holds the token usage from a compaction LLM call.
type compactUsage struct {
	InputTokens  int
	OutputTokens int
	CostCents    int64
}

// compactMessages performs LLM-based conversation compaction.
// It takes the older messages, summarizes them via a lightweight LLM call,
// and returns a new message list with the summary prepended to the recent messages.
// The returned compactUsage tracks the cost of the summarization call.
func compactMessages(ctx context.Context, rt *Runtime, messages []llm.Message, model string, cfg RunConfig) ([]llm.Message, *compactUsage) {
	if len(messages) <= CompactKeepRecent {
		return messages, nil // nothing to compact
	}

	// Split into old (to summarize) and recent (to keep verbatim).
	splitIdx := len(messages) - CompactKeepRecent
	oldMessages := messages[:splitIdx]
	recentMessages := messages[splitIdx:]

	// Build a text representation of the old messages for the summarizer.
	var oldText strings.Builder
	for _, msg := range oldMessages {
		oldText.WriteString(fmt.Sprintf("[%s]: ", msg.Role))
		for _, part := range msg.Content {
			switch part.Type {
			case "text":
				text := safeRuneTruncate(part.Text, 1000)
				oldText.WriteString(text)
			case "tool_call":
				oldText.WriteString(fmt.Sprintf("[Tool: %s(%s)]", part.ToolName, truncateArgs(part.ArgumentsJSON)))
			case "tool_result":
				text := safeRuneTruncate(part.Text, 500)
				oldText.WriteString(fmt.Sprintf("[Result from %s: %s]", part.ToolName, text))
			}
			oldText.WriteString(" ")
		}
		oldText.WriteString("\n")
	}

	// Use a lightweight model to summarize — try the file_explorer model (cheapest),
	// fall back to the current model.
	summaryModel := model
	usedCheapModel := false
	if fe, ok := rt.agentRegistry.Get("file_explorer"); ok {
		resolved := fe
		if cfg.CostMode != "" && cfg.ModelResolver != nil {
			resolved = fe.WithCostMode(cfg.CostMode, cfg.ModelResolver)
		}
		summaryModel = resolved.Model
		usedCheapModel = true
	}

	provider, routedModel, err := rt.llmRegistry.Route(summaryModel)
	if err != nil {
		if usedCheapModel {
			rt.log.Warn().Str("model", summaryModel).Msg("cheap model unavailable for compaction, falling back to current model")
		}
		// Fall back to current model if the cheap model isn't available.
		provider, routedModel, err = rt.llmRegistry.Route(model)
		if err != nil {
			// Last resort: just do truncation-based compression.
			return truncateCompact(messages), nil
		}
	}

	systemPrompt := `You are a conversation summarizer. Summarize the following conversation between a user and an AI coding assistant. Preserve:
1. All file paths mentioned and what was done to them
2. Key decisions made and their rationale
3. Important code changes, function names, and variable names
4. Any errors encountered and how they were resolved
5. The user's original request and current goal

Be concise but complete. Use bullet points. Do NOT omit file paths or function names.`

	maxTokens := compactSummaryMaxTokens
	req := &llm.CompletionRequest{
		Model: routedModel,
		Messages: []llm.Message{
			{
				Role: "user",
				Content: []llm.ContentPart{
					{Type: "text", Text: "Summarize this conversation history:\n\n" + oldText.String()},
				},
			},
		},
		SystemPrompt: &systemPrompt,
		MaxTokens:    &maxTokens,
	}

	ch, err := provider.StreamCompletion(ctx, req)
	if err != nil {
		// If LLM call fails, fall back to truncation-based compression.
		return truncateCompact(messages), nil
	}

	var summary strings.Builder
	var compactUsg compactUsage
	for ev := range ch {
		if ev.Delta != nil {
			summary.WriteString(ev.Delta.Text)
		}
		if ev.Complete != nil {
			compactUsg.InputTokens = ev.Complete.Usage.InputTokens
			compactUsg.OutputTokens = ev.Complete.Usage.OutputTokens
			compactUsg.CostCents = ev.Complete.Usage.CostCents
		}
		if ev.Error != nil && !ev.Error.Retryable {
			// LLM error — fall back to truncation.
			return truncateCompact(messages), nil
		}
	}

	// Build the compacted message list: summary as a system-like user message + recent messages.
	compacted := make([]llm.Message, 0, CompactKeepRecent+2)
	compacted = append(compacted, llm.Message{
		Role: "user",
		Content: []llm.ContentPart{
			{Type: "text", Text: fmt.Sprintf("[Context Summary — earlier conversation compressed]\n\n%s\n\n[End of summary. The conversation continues below.]", summary.String())},
		},
	})

	// Add synthetic assistant response to maintain proper role alternation.
	// Only add if the first recent message is not a user message (to avoid user/user collision).
	if len(recentMessages) == 0 || recentMessages[0].Role != "user" {
		compacted = append(compacted, llm.Message{
			Role: "assistant",
			Content: []llm.ContentPart{
				{Type: "text", Text: "Understood. I have the context from our earlier conversation. Let me continue from where we left off."},
			},
		})
	}

	// Ensure proper role alternation: if the first recent message is the same role
	// as the last compacted message, insert a bridging message.
	if len(recentMessages) > 0 && len(compacted) > 0 {
		lastRole := compacted[len(compacted)-1].Role
		nextRole := recentMessages[0].Role
		if lastRole == nextRole {
			bridgeRole := "assistant"
			if lastRole == "assistant" {
				bridgeRole = "user"
			}
			compacted = append(compacted, llm.Message{
				Role: bridgeRole,
				Content: []llm.ContentPart{
					{Type: "text", Text: "[continuation]"},
				},
			})
		}
	}

	compacted = append(compacted, recentMessages...)

	return compacted, &compactUsg
}

// maxTruncateSummaryChars caps the total size of a truncation-based summary
// to prevent it from consuming too much context.
const maxTruncateSummaryChars = 8000

// truncateCompact is a fallback compaction that aggressively truncates old messages
// when the LLM summarizer is unavailable.
func truncateCompact(messages []llm.Message) []llm.Message {
	if len(messages) <= CompactKeepRecent {
		return messages
	}

	splitIdx := len(messages) - CompactKeepRecent
	oldMessages := messages[:splitIdx]
	recentMessages := messages[splitIdx:]

	// Build a simple extractive summary from old messages.
	var summary strings.Builder
	summary.WriteString("[Context Summary — earlier conversation compressed]\n\n")

	for _, msg := range oldMessages {
		if summary.Len() >= maxTruncateSummaryChars {
			summary.WriteString("\n... [older messages omitted]\n")
			break
		}
		// Include tool results too, not just user/assistant text.
		for _, part := range msg.Content {
			if part.Type == "text" && len(part.Text) > 0 {
				text := safeRuneTruncate(part.Text, 200)
				summary.WriteString(fmt.Sprintf("- %s: %s\n", msg.Role, text))
			} else if part.Type == "tool_result" && len(part.Text) > 0 {
				text := safeRuneTruncate(part.Text, 150)
				summary.WriteString(fmt.Sprintf("- [%s result]: %s\n", part.ToolName, text))
			}
		}
	}

	summary.WriteString("\n[End of summary. The conversation continues below.]")

	compacted := make([]llm.Message, 0, CompactKeepRecent+2)
	compacted = append(compacted, llm.Message{
		Role: "user",
		Content: []llm.ContentPart{
			{Type: "text", Text: summary.String()},
		},
	})

	// Add assistant bridge and ensure role alternation (same logic as LLM path).
	if len(recentMessages) == 0 || recentMessages[0].Role != "user" {
		compacted = append(compacted, llm.Message{
			Role: "assistant",
			Content: []llm.ContentPart{
				{Type: "text", Text: "Understood. I have the context from our earlier conversation. Continuing."},
			},
		})
	}
	if len(recentMessages) > 0 && len(compacted) > 0 {
		lastRole := compacted[len(compacted)-1].Role
		if lastRole == recentMessages[0].Role {
			bridgeRole := "assistant"
			if lastRole == "assistant" {
				bridgeRole = "user"
			}
			compacted = append(compacted, llm.Message{
				Role: bridgeRole,
				Content: []llm.ContentPart{
					{Type: "text", Text: "[continuation]"},
				},
			})
		}
	}

	compacted = append(compacted, recentMessages...)

	return compacted
}

// safeRuneTruncate truncates a string to maxRunes runes, ensuring valid UTF-8.
// Appends "... [truncated]" if the string was truncated.
func safeRuneTruncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "... [truncated]"
}

// truncateArgs shortens tool arguments for display in summaries.
func truncateArgs(args string) string {
	return safeRuneTruncate(args, 100)
}
