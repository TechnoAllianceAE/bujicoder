package agentruntime

import (
	"testing"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// assistantWithToolCall builds an assistant message carrying a tool_call part.
func assistantWithToolCall(id string) llm.Message {
	return llm.Message{
		Role: "assistant",
		Content: []llm.ContentPart{
			{Type: "tool_call", ToolCallID: id, ToolName: "read_files", ArgumentsJSON: `{"paths":["a.go"]}`},
		},
	}
}

// toolResultMessage builds the tool message answering a tool_call.
func toolResultMessage(id string) llm.Message {
	return llm.Message{
		Role: "tool",
		Content: []llm.ContentPart{
			{Type: "tool_result", ToolCallID: id, ToolName: "read_files", Text: "contents"},
		},
	}
}

// orphanToolResults returns the tool_call IDs of every tool_result in msgs that
// has no preceding assistant tool_call with the same ID. Providers reject those
// (Anthropic: 400 "unexpected tool_use_id").
func orphanToolResults(msgs []llm.Message) []string {
	seen := make(map[string]bool)
	var orphans []string
	for _, m := range msgs {
		for _, p := range m.Content {
			switch p.Type {
			case "tool_call":
				seen[p.ToolCallID] = true
			case "tool_result":
				if !seen[p.ToolCallID] {
					orphans = append(orphans, p.ToolCallID)
				}
			}
		}
	}
	return orphans
}

// conversation builds n user/assistant exchanges followed by the given tail.
func conversation(prefix int, tail ...llm.Message) []llm.Message {
	msgs := make([]llm.Message, 0, prefix*2+len(tail))
	for range prefix {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: []llm.ContentPart{{Type: "text", Text: "q"}}},
			llm.Message{Role: "assistant", Content: []llm.ContentPart{{Type: "text", Text: "a"}}},
		)
	}
	return append(msgs, tail...)
}

func TestCompactSplitIndexNeverOrphansToolResults(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
	}{
		{
			// prefix 12 + assistant(tc-1) at 12 + tool(tc-1) at 13, total 21:
			// the nominal split (21-8) is exactly the tool message.
			name: "tool result lands exactly on the split boundary",
			messages: conversation(6,
				assistantWithToolCall("tc-1"),
				toolResultMessage("tc-1"),
				llm.Message{Role: "assistant", Content: []llm.ContentPart{{Type: "text", Text: "done"}}},
				llm.Message{Role: "user", Content: []llm.ContentPart{{Type: "text", Text: "next"}}},
				llm.Message{Role: "assistant", Content: []llm.ContentPart{{Type: "text", Text: "ok"}}},
				llm.Message{Role: "user", Content: []llm.ContentPart{{Type: "text", Text: "again"}}},
				assistantWithToolCall("tc-2"),
				toolResultMessage("tc-2"),
				llm.Message{Role: "assistant", Content: []llm.ContentPart{{Type: "text", Text: "final"}}},
			),
		},
		{
			// prefix 10 + assistant(tc-1) at 10 + two tool messages at 11,12,
			// total 19: the nominal split (19-8) is the first tool message.
			name: "several consecutive tool messages at the boundary",
			messages: conversation(5,
				assistantWithToolCall("tc-1"),
				toolResultMessage("tc-1"),
				toolResultMessage("tc-1"),
				llm.Message{Role: "assistant", Content: []llm.ContentPart{{Type: "text", Text: "x"}}},
				llm.Message{Role: "user", Content: []llm.ContentPart{{Type: "text", Text: "y"}}},
				llm.Message{Role: "assistant", Content: []llm.ContentPart{{Type: "text", Text: "z"}}},
				llm.Message{Role: "user", Content: []llm.ContentPart{{Type: "text", Text: "w"}}},
				llm.Message{Role: "assistant", Content: []llm.ContentPart{{Type: "text", Text: "v"}}},
				llm.Message{Role: "user", Content: []llm.ContentPart{{Type: "text", Text: "u"}}},
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Guard the fixture: the naive split must hit a tool message,
			// otherwise the test would not exercise the bug.
			if naive := len(tt.messages) - CompactKeepRecent; tt.messages[naive].Role != "tool" {
				t.Fatalf("fixture is wrong: naive split index %d is a %q message", naive, tt.messages[naive].Role)
			}

			idx := compactSplitIndex(tt.messages)
			if idx < len(tt.messages) && tt.messages[idx].Role == "tool" {
				t.Fatalf("split index %d starts on a tool message", idx)
			}

			got := truncateCompact(tt.messages)
			if orphans := orphanToolResults(got); len(orphans) > 0 {
				t.Fatalf("compaction produced orphaned tool results: %v", orphans)
			}
		})
	}
}

func TestTruncateCompactKeepsRecentMessages(t *testing.T) {
	msgs := conversation(10)
	last := msgs[len(msgs)-1]

	got := truncateCompact(msgs)

	if len(got) >= len(msgs) {
		t.Fatalf("compaction did not shrink history: %d -> %d", len(msgs), len(got))
	}
	if got[len(got)-1].Content[0].Text != last.Content[0].Text {
		t.Error("most recent message was dropped by compaction")
	}
	if got[0].Role != "user" {
		t.Errorf("summary message role = %q, want user", got[0].Role)
	}
}
