package codeintel

// extractRustSymbols extracts Rust symbols using regex patterns.
func extractRustSymbols(content string) []Symbol {
	return extractWithPatterns(content, rustPatterns)
}

var rustPatterns = []symbolPattern{
	// Struct definitions
	{kind: "type", pattern: `^(?:pub\s+)?struct\s+(\w+)`},
	// Enum definitions
	{kind: "type", pattern: `^(?:pub\s+)?enum\s+(\w+)`},
	// Trait definitions (like interfaces)
	{kind: "interface", pattern: `^(?:pub\s+)?trait\s+(\w+)`},
	// Free functions
	{kind: "function", pattern: `^(?:pub\s+)?(?:async\s+)?fn\s+(\w+)`},
	// Methods in impl blocks
	{kind: "method", pattern: `^\s+(?:pub\s+)?(?:async\s+)?fn\s+(\w+)`},
	// Type aliases
	{kind: "type", pattern: `^(?:pub\s+)?type\s+(\w+)`},
	// Constants
	{kind: "variable", pattern: `^(?:pub\s+)?const\s+(\w+)`},
	// Static variables
	{kind: "variable", pattern: `^(?:pub\s+)?static\s+(?:mut\s+)?(\w+)`},
}
