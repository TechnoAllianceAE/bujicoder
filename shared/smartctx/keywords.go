package smartctx

import (
	"strings"
	"unicode"
)

// stopWords are common English words that don't help with file matching.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"being": true, "have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "must": true, "shall": true,
	"i": true, "you": true, "he": true, "she": true, "it": true, "we": true, "they": true,
	"me": true, "him": true, "her": true, "us": true, "them": true,
	"my": true, "your": true, "his": true, "its": true, "our": true, "their": true,
	"this": true, "that": true, "these": true, "those": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "with": true,
	"from": true, "by": true, "about": true, "into": true, "of": true,
	"not": true, "no": true, "if": true, "then": true, "else": true,
	"what": true, "how": true, "why": true, "when": true, "where": true, "which": true,
	"can": true, "need": true, "want": true, "please": true, "help": true,
	"add": true, "fix": true, "update": true, "change": true, "make": true,
	"create": true, "implement": true, "write": true, "use": true,
}

// ExtractKeywords extracts meaningful keywords from a natural-language query.
// It splits on whitespace and punctuation, lowercases, removes stop words,
// and splits camelCase/PascalCase identifiers.
func ExtractKeywords(query string) []string {
	if query == "" {
		return nil
	}

	// Split on whitespace and common delimiters.
	words := tokenize(query)

	seen := make(map[string]bool)
	var keywords []string

	for _, word := range words {
		word = strings.ToLower(word)
		if len(word) < 2 {
			continue
		}
		if stopWords[word] {
			continue
		}
		if seen[word] {
			continue
		}
		seen[word] = true
		keywords = append(keywords, word)

		// Also split camelCase and add parts.
		parts := splitCamelCase(word)
		for _, part := range parts {
			part = strings.ToLower(part)
			if len(part) < 2 || stopWords[part] || seen[part] {
				continue
			}
			seen[part] = true
			keywords = append(keywords, part)
		}
	}

	return keywords
}

// tokenize splits text on non-alphanumeric characters.
func tokenize(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// splitCamelCase splits "handleRequest" into ["handle", "Request"].
func splitCamelCase(s string) []string {
	if len(s) <= 1 {
		return nil
	}

	var parts []string
	runes := []rune(s)
	start := 0

	for i := 1; i < len(runes); i++ {
		if unicode.IsUpper(runes[i]) && !unicode.IsUpper(runes[i-1]) {
			part := string(runes[start:i])
			if len(part) > 1 {
				parts = append(parts, part)
			}
			start = i
		}
	}

	// Don't add the last segment — it was already in the original word.
	if start > 0 && start < len(runes) {
		part := string(runes[start:])
		if len(part) > 1 {
			parts = append(parts, part)
		}
	}

	if len(parts) <= 1 {
		return nil // No split happened
	}
	return parts
}
