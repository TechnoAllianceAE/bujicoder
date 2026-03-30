package agentruntime

import (
	"fmt"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// state holds the mutable conversation state for a single agent run.
type state struct {
	messages       []llm.Message
	dynamicCtx     string // cached dynamic context (file tree, git, knowledge) built once per run
	toolCallCounts map[string]int // track repeated tool calls: "toolName:argsHash" -> count
}

func newState(cfg RunConfig) *state {
	s := &state{
		toolCallCounts: make(map[string]int),
	}

	// Copy existing history
	if len(cfg.History) > 0 {
		s.messages = make([]llm.Message, len(cfg.History))
		copy(s.messages, cfg.History)
	}

	// Append the user message if provided
	if cfg.UserMessage != "" {
		parts := []llm.ContentPart{
			{Type: "text", Text: cfg.UserMessage},
		}
		// Attach any image parts (from @path references in user input)
		parts = append(parts, cfg.UserImages...)
		s.messages = append(s.messages, llm.Message{
			Role:    "user",
			Content: parts,
		})
	}

	return s
}

// appendAssistantText adds an assistant text message.
func (s *state) appendAssistantText(text string) {
	s.messages = append(s.messages, llm.Message{
		Role: "assistant",
		Content: []llm.ContentPart{
			{Type: "text", Text: text},
		},
	})
}

// appendAssistantToolCalls adds an assistant message with tool calls.
func (s *state) appendAssistantToolCalls(text string, toolCalls []llm.ContentPart) {
	var parts []llm.ContentPart
	if text != "" {
		parts = append(parts, llm.ContentPart{Type: "text", Text: text})
	}
	parts = append(parts, toolCalls...)
	s.messages = append(s.messages, llm.Message{
		Role:    "assistant",
		Content: parts,
	})
}

// appendToolResults adds tool result messages.
func (s *state) appendToolResults(results []llm.ContentPart) {
	s.messages = append(s.messages, llm.Message{
		Role:    "tool",
		Content: results,
	})
}

// Context compression constants
const (
	KeepUncompressedSteps = 6    // Number of recent messages to keep fully intact
	MaxToolResultSize     = 2000 // If older tool result is > 2000 chars, compress it
	TruncatedSize         = 500  // Keep the first 500 chars of a compressed result
)

// CompressHistory iterates through old messages and truncates massive tool results
// to prevent context window overflow during long-running tasks.
func (s *state) CompressHistory() {
	if len(s.messages) <= KeepUncompressedSteps {
		return
	}

	// Only compress messages older than the uncompressed window
	compressLimit := len(s.messages) - KeepUncompressedSteps

	for i := 0; i < compressLimit; i++ {
		msg := &s.messages[i]
		if msg.Role != "tool" {
			continue // Only compress tool results (file reads, grep output, etc.)
		}

		for j := range msg.Content {
			part := &msg.Content[j]
			if part.Type == "text" && len(part.Text) > MaxToolResultSize {
				removed := len(part.Text) - TruncatedSize
				part.Text = part.Text[:TruncatedSize] + fmt.Sprintf("\n\n... [Truncated for brevity. %d characters removed.]", removed)
			}
		}
	}
}
