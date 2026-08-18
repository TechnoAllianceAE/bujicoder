package codeintel

import (
	"regexp"
	"strings"
)

// maxScanLines bounds the number of lines a regex extractor will examine. A
// single generated or minified file must not turn indexing into an O(n^2) stall.
const maxScanLines = 20000

// symbolPattern defines a compiled regex for extracting symbols.
// The first capture group is the symbol name.
type symbolPattern struct {
	kind string // "function", "class", "method", "type", "interface", "variable"
	re   *regexp.Regexp
}

// pat builds a symbolPattern. Patterns are compiled once at package
// initialization rather than on every parsed file.
func pat(kind, expr string) symbolPattern {
	return symbolPattern{kind: kind, re: regexp.MustCompile(expr)}
}

// extractWithPatterns applies regex patterns to extract symbols from source code.
// It handles multi-line constructs by tracking brace depth to determine end lines.
func extractWithPatterns(content string, patterns []symbolPattern) []Symbol {
	lines := strings.Split(content, "\n")
	if len(lines) > maxScanLines {
		lines = lines[:maxScanLines]
	}

	var symbols []Symbol

	for lineIdx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		for _, p := range patterns {
			// Matched against the raw line: several patterns are anchored on
			// leading indentation to tell a method from a free function, which
			// can never match a trimmed line.
			matches := p.re.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}

			symbols = append(symbols, Symbol{
				Name:      matches[1],
				Kind:      p.kind,
				StartLine: lineIdx + 1,
				EndLine:   findBlockEnd(lines, lineIdx),
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

// truncateLine truncates a line to maxLen runes. Cutting on a byte boundary
// would split a multi-byte rune and emit invalid UTF-8 in the signature.
func truncateLine(line string, maxLen int) string {
	if maxLen < 4 || len(line) <= maxLen {
		return line
	}
	count := 0
	for i := range line {
		if count == maxLen-3 {
			return line[:i] + "..."
		}
		count++
	}
	return line
}
