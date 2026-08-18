package agentruntime

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// filler returns a string of n 'x' bytes.
func filler(n int) string { return strings.Repeat("x", n) }

// padMessages returns n throwaway user messages so that earlier messages fall
// outside the KeepUncompressedSteps window.
func padMessages(n int) []llm.Message {
	out := make([]llm.Message, 0, n)
	for range n {
		out = append(out, llm.Message{
			Role:    "user",
			Content: []llm.ContentPart{{Type: "text", Text: "pad"}},
		})
	}
	return out
}

func TestCompressHistoryTruncatesToolResults(t *testing.T) {
	big := filler(MaxToolResultSize * 2)

	tests := []struct {
		name      string
		partType  string
		text      string
		wantTrunc bool
	}{
		{"tool_result part is compressed", "tool_result", big, true},
		{"text part is compressed", "text", big, true},
		{"small tool_result is left alone", "tool_result", filler(10), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &state{}
			s.messages = append(s.messages, llm.Message{
				Role:    "tool",
				Content: []llm.ContentPart{{Type: tt.partType, ToolCallID: "c1", Text: tt.text}},
			})
			s.messages = append(s.messages, padMessages(KeepUncompressedSteps)...)

			s.CompressHistory()

			got := s.messages[0].Content[0].Text
			truncated := strings.Contains(got, "[Truncated for brevity.")
			if truncated != tt.wantTrunc {
				t.Fatalf("truncated = %v, want %v (len %d)", truncated, tt.wantTrunc, len(got))
			}
			if tt.wantTrunc && len(got) > TruncatedSize+128 {
				t.Errorf("compressed text is %d bytes, want ~%d", len(got), TruncatedSize)
			}
		})
	}
}

func TestCompressHistoryDoesNotMutateCallerHistory(t *testing.T) {
	for _, partType := range []string{"tool_result", "text"} {
		t.Run(partType, func(t *testing.T) {
			original := filler(MaxToolResultSize * 2)

			history := []llm.Message{
				{Role: "tool", Content: []llm.ContentPart{{Type: partType, ToolCallID: "c1", Text: original}}},
			}
			history = append(history, padMessages(KeepUncompressedSteps)...)

			s := newState(RunConfig{History: history})
			s.CompressHistory()

			if got := history[0].Content[0].Text; got != original {
				t.Fatalf("caller history was mutated: len %d, want %d", len(got), len(original))
			}
			if got := s.messages[0].Content[0].Text; got == original {
				t.Fatal("run state was not compressed")
			}
		})
	}
}

func TestCompressHistoryKeepsValidUTF8(t *testing.T) {
	// "€" is 3 bytes, so a raw byte cut at TruncatedSize lands mid-rune.
	text := strings.Repeat("€", MaxToolResultSize)

	s := &state{}
	s.messages = append(s.messages, llm.Message{
		Role:    "tool",
		Content: []llm.ContentPart{{Type: "tool_result", ToolCallID: "c1", Text: text}},
	})
	s.messages = append(s.messages, padMessages(KeepUncompressedSteps)...)

	s.CompressHistory()

	got := s.messages[0].Content[0].Text
	if !utf8.ValidString(got) {
		t.Fatalf("compressed text is not valid UTF-8: %q", got[:32])
	}
}
