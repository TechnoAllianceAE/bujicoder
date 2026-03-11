package editmatch

import (
	"strings"
	"testing"
)

func TestExactMatch(t *testing.T) {
	content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	oldStr := "fmt.Println(\"hello\")"

	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected a match")
	}
	if r.Strategy != "exact" {
		t.Fatalf("expected exact strategy, got %s", r.Strategy)
	}
	if r.Matched != oldStr {
		t.Fatalf("matched text mismatch: %q", r.Matched)
	}
}

func TestExactMatch_NoMatch(t *testing.T) {
	content := "func main() {}\n"
	oldStr := "func foo() {}"

	r := Find(content, oldStr)
	if r != nil {
		t.Fatal("expected no match")
	}
}

func TestLineTrimmed(t *testing.T) {
	// Content has trailing spaces; search string doesn't.
	content := "func main() {   \n\tfmt.Println(\"hello\")   \n}   \n"
	oldStr := "func main() {\n\tfmt.Println(\"hello\")\n}"

	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected a match")
	}
	if r.Strategy != "line_trimmed" {
		t.Fatalf("expected line_trimmed, got %s", r.Strategy)
	}
}

func TestWhitespaceNormalized(t *testing.T) {
	// Content uses tabs; search uses spaces and different newlines.
	content := "if err != nil {\n\t\treturn  err\n\t}\n"
	oldStr := "if err != nil { return err }"

	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected a match")
	}
	if r.Strategy != "whitespace_normalized" {
		t.Fatalf("expected whitespace_normalized, got %s", r.Strategy)
	}
}

func TestIndentationFlexible(t *testing.T) {
	// LLM sends wrong indentation level — whitespace_normalized may also catch this.
	// The important thing is that a match IS found.
	content := "\t\tif x > 0 {\n\t\t\treturn true\n\t\t}\n"
	oldStr := "if x > 0 {\n\treturn true\n}\n"

	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected a match")
	}
	// Accept either whitespace_normalized (strategy 3) or indentation_flexible (strategy 4).
	validStrategies := map[string]bool{
		"whitespace_normalized":  true,
		"indentation_flexible":   true,
	}
	if !validStrategies[r.Strategy] {
		t.Fatalf("expected whitespace_normalized or indentation_flexible, got %s", r.Strategy)
	}
}

func TestEscapeNormalized(t *testing.T) {
	// Content has \r\n line endings; search has \n.
	content := "line one\r\nline two\r\nline three\r\n"
	oldStr := "line one\nline two\nline three"

	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected a match")
	}
	// Should be found by escape normalization (or earlier strategy).
	if r.Strategy != "escape_normalized" {
		t.Logf("matched by %s (also acceptable if earlier strategy matched)", r.Strategy)
	}
}

func TestBlockAnchor(t *testing.T) {
	// Search string has a minor typo that prevents exact match.
	content := "func calculate(x int) int {\n\tresult := x * 2\n\treturn result\n}\n"
	oldStr := "func calculate(x int) int {\n\tresult := x * 3\n\treturn result\n}\n"

	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected block_anchor match")
	}
	if r.Strategy != "block_anchor" {
		t.Fatalf("expected block_anchor, got %s", r.Strategy)
	}
	// The matched text should be the actual content block.
	if !strings.Contains(r.Matched, "x * 2") {
		t.Fatalf("expected matched text to contain actual content, got: %q", r.Matched)
	}
}

func TestBlockAnchor_TooMuchDifference(t *testing.T) {
	// Completely different content — should not match.
	content := "func foo() {\n\treturn 1\n}\n"
	oldStr := "func bar() {\n\tsome completely different code\n\twith many lines\n\tthat are nothing alike\n}\n"

	r := Find(content, oldStr)
	// Should not match because edit distance is too high.
	if r != nil && r.Strategy == "block_anchor" {
		t.Fatal("should not match with high edit distance")
	}
}

func TestFind_EmptyOldStr(t *testing.T) {
	r := Find("some content", "")
	// Empty search should still find (exact match of empty string).
	// But our strategies skip empty normalized strings.
	if r != nil {
		t.Log("empty old_str returned a match (acceptable)")
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		dist int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"", "abc", 3},
		{"abc", "", 3},
	}
	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.dist {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.dist)
		}
	}
}

func TestFind_PrefersEarlyStrategy(t *testing.T) {
	// An exact match exists — should use exact, not a later strategy.
	content := "hello world"
	oldStr := "hello world"

	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected match")
	}
	if r.Strategy != "exact" {
		t.Fatalf("expected exact strategy when exact match exists, got %s", r.Strategy)
	}
}

func TestFind_RealWorldIndentMismatch(t *testing.T) {
	// Real-world scenario: LLM sends code with 4-space indent,
	// but file uses tabs.
	content := `func processItems(items []Item) {
	for _, item := range items {
		if item.Valid {
			process(item)
		}
	}
}
`
	oldStr := `func processItems(items []Item) {
    for _, item := range items {
        if item.Valid {
            process(item)
        }
    }
}
`
	r := Find(content, oldStr)
	if r == nil {
		t.Fatal("expected match for indent mismatch")
	}
	// Should be found by indentation_flexible or earlier.
	t.Logf("matched by strategy: %s", r.Strategy)
}
