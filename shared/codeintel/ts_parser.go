package codeintel

// extractTSSymbols extracts TypeScript/JavaScript symbols using regex patterns.
func extractTSSymbols(content string) []Symbol {
	return extractWithPatterns(content, tsPatterns)
}

var tsPatterns = []symbolPattern{
	// Classes
	{kind: "class", pattern: `^(?:export\s+)?class\s+(\w+)`},
	// Interfaces (TS only, but safe to apply to JS too — won't match)
	{kind: "interface", pattern: `^(?:export\s+)?interface\s+(\w+)`},
	// Type aliases
	{kind: "type", pattern: `^(?:export\s+)?type\s+(\w+)\s*[=<]`},
	// Regular functions
	{kind: "function", pattern: `^(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*[<(]`},
	// Arrow functions assigned to const/let/var
	{kind: "function", pattern: `^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`},
	{kind: "function", pattern: `^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z_]\w*)\s*=>`},
	// Enum
	{kind: "type", pattern: `^(?:export\s+)?enum\s+(\w+)`},
	// React components (common pattern: export function/const ComponentName)
	{kind: "function", pattern: `^(?:export\s+)?(?:default\s+)?function\s+([A-Z]\w+)`},
}
