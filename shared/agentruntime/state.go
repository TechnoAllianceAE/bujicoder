package agentruntime

import (
	"fmt"
	"unicode/utf8"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// state holds the mutable conversation state for a single agent run.
type state struct {
	messages       []llm.Message
	dynamicCtx     string         // cached dynamic context (file tree, git, knowledge) built once per run
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
//
// Message content slices may be shared with the caller-supplied history (newState
// copies the message headers, not the underlying ContentPart slices), so a message
// is copy-on-written before any part of it is truncated. Mutating in place would
// corrupt the caller's conversation record.
func (s *state) CompressHistory() {
	if len(s.messages) <= KeepUncompressedSteps {
		return
	}

	// Only compress messages older than the uncompressed window
	compressLimit := len(s.messages) - KeepUncompressedSteps

	for i := range compressLimit {
		msg := &s.messages[i]
		if msg.Role != "tool" {
			continue // Only compress tool results (file reads, grep output, etc.)
		}

		copied := false
		for j := range msg.Content {
			// Tool results are stored as "tool_result" parts; older code only
			// looked at "text" parts, which never appear in a tool message.
			if t := msg.Content[j].Type; t != "tool_result" && t != "text" {
				continue
			}
			if len(msg.Content[j].Text) <= MaxToolResultSize {
				continue
			}
			if !copied {
				parts := make([]llm.ContentPart, len(msg.Content))
				copy(parts, msg.Content)
				msg.Content = parts
				copied = true
			}
			part := &msg.Content[j]
			kept := safeRuneTruncateRaw(part.Text, TruncatedSize)
			removed := len(part.Text) - len(kept)
			part.Text = kept + fmt.Sprintf("\n\n... [Truncated for brevity. %d characters removed.]", removed)
		}
	}
}

// safeRuneTruncateRaw returns the first maxBytes bytes of s, backing up to the
// nearest rune boundary so the result is always valid UTF-8. No marker is added.
func safeRuneTruncateRaw(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
