package codeintel

// extractTSSymbols extracts TypeScript/JavaScript symbols using regex patterns.
func extractTSSymbols(content string) []Symbol {
	return extractWithPatterns(content, tsPatterns)
}

var tsPatterns = []symbolPattern{
	// Classes
	pat("class", `^(?:export\s+)?class\s+(\w+)`),
	// Interfaces (TS only, but safe to apply to JS too — won't match)
	pat("interface", `^(?:export\s+)?interface\s+(\w+)`),
	// Type aliases
	pat("type", `^(?:export\s+)?type\s+(\w+)\s*[=<]`),
	// Regular functions
	pat("function", `^(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*[<(]`),
	// Arrow functions assigned to const/let/var
	pat("function", `^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`),
	pat("function", `^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z_]\w*)\s*=>`),
	// Enum
	pat("type", `^(?:export\s+)?enum\s+(\w+)`),
	// React components (common pattern: export function/const ComponentName)
	pat("function", `^(?:export\s+)?(?:default\s+)?function\s+([A-Z]\w+)`),
}
