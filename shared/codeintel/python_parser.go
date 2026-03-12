package codeintel

// extractPythonSymbols extracts Python symbols using regex patterns.
func extractPythonSymbols(content string) []Symbol {
	return extractWithPatterns(content, pythonPatterns)
}

var pythonPatterns = []symbolPattern{
	{kind: "class", pattern: `^class\s+(\w+)\s*[\(:]`},
	{kind: "function", pattern: `^def\s+(\w+)\s*\(`},
	{kind: "method", pattern: `^\s{4}def\s+(\w+)\s*\(self`},
	{kind: "function", pattern: `^async\s+def\s+(\w+)\s*\(`},
	{kind: "variable", pattern: `^(\w+)\s*:\s*\w+\s*=`},
	{kind: "variable", pattern: `^([A-Z_][A-Z0-9_]+)\s*=`},
}
