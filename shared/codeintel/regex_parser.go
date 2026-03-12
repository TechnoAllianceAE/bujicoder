package codeintel

import (
	"regexp"
	"strings"
)

// symbolPattern defines a regex pattern for extracting symbols.
type symbolPattern struct {
	kind    string // "function", "class", "method", "type", "interface", "variable"
	pattern string // regex pattern (first capture group = symbol name)
}

// extractWithPatterns applies regex patterns to extract symbols from source code.
// It handles multi-line constructs by tracking brace depth to determine end lines.
func extractWithPatterns(content string, patterns []symbolPattern) []Symbol {
	lines := strings.Split(content, "\n")
	compiled := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		compiled[i] = regexp.MustCompile(p.pattern)
	}

	var symbols []Symbol

	for lineIdx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		for i, re := range compiled {
			matches := re.FindStringSubmatch(trimmed)
			if len(matches) < 2 {
				continue
			}

			name := matches[1]
			startLine := lineIdx + 1
			endLine := findBlockEnd(lines, lineIdx)

			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      patterns[i].kind,
				StartLine: startLine,
				EndLine:   endLine,
				Signature: truncateLine(trimmed, 120),
			})
			break // First pattern match wins for this line
		}
	}

	return symbols
}

// findBlockEnd finds the end of a code block by tracking brace/indent depth.
func findBlockEnd(lines []string, startIdx int) int {
	depth := 0
	foundOpen := false

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		for _, ch := range line {
			switch ch {
			case '{':
				depth++
				foundOpen = true
			case '}':
				depth--
			}
		}
		if foundOpen && depth <= 0 {
			return i + 1
		}
	}

	// For languages using indentation (Python), look for next same-level line.
	if !foundOpen {
		return findIndentBlockEnd(lines, startIdx)
	}

	return startIdx + 1
}

// findIndentBlockEnd finds the end of a Python-style indented block.
func findIndentBlockEnd(lines []string, startIdx int) int {
	if startIdx >= len(lines)-1 {
		return startIdx + 1
	}

	// Get the indentation of the line after the declaration.
	baseIndent := getIndent(lines[startIdx])

	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // Skip blank lines and comments
		}
		lineIndent := getIndent(line)
		if lineIndent <= baseIndent {
			return i // Block ended
		}
	}

	return len(lines) // Block extends to end of file
}

// getIndent returns the number of leading spaces (tabs count as 4).
func getIndent(line string) int {
	indent := 0
	for _, ch := range line {
		switch ch {
		case ' ':
			indent++
		case '\t':
			indent += 4
		default:
			return indent
		}
	}
	return indent
}

// truncateLine truncates a line to maxLen characters.
func truncateLine(line string, maxLen int) string {
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen-3] + "..."
}
