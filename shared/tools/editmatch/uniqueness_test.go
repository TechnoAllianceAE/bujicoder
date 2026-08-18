package editmatch

import "testing"

// TestFind_RejectsAmbiguous covers the core safety contract: when the search
// string is empty or matches more than one site, Find must report no match
// rather than silently picking an occurrence for the caller to overwrite.
func TestFind_RejectsAmbiguous(t *testing.T) {
	tests := []struct {
		name    string
		content string
		oldStr  string
	}{
		{
			name:    "duplicate exact occurrences",
			content: "func a() { log() }\nfunc b() { log() }\n",
			oldStr:  "log()",
		},
		{
			name:    "duplicate multiline blocks",
			content: "if err != nil {\n\treturn err\n}\nx := 1\nif err != nil {\n\treturn err\n}\n",
			oldStr:  "if err != nil {\n\treturn err\n}",
		},
		{
			name:    "duplicate differing only in trailing whitespace",
			content: "value = 1   \nvalue = 1\n",
			oldStr:  "value = 1",
		},
		{
			name:    "empty search string",
			content: "package main\n",
			oldStr:  "",
		},
		{
			name:    "empty content and empty search",
			content: "",
			oldStr:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r := Find(tt.content, tt.oldStr); r != nil {
				t.Fatalf("Find matched ambiguously at [%d,%d) via %s", r.Start, r.End, r.Strategy)
			}
		})
	}
}

// TestFind_SpansAreSliceable guards the callers, which all do
// content[:r.Start] + new + content[r.End:].
func TestFind_SpansAreSliceable(t *testing.T) {
	tests := []struct {
		name    string
		content string
		oldStr  string
	}{
		{"exact", "alpha beta gamma", "beta"},
		{"crlf content", "line one\r\nline two\r\nline three\r\n", "line one\nline two\nline three"},
		{"trailing whitespace", "func main() {   \n\treturn   \n}   \n", "func main() {\n\treturn\n}"},
		{"indent mismatch", "\t\tif x {\n\t\t\ty()\n\t\t}\n", "if x {\n\ty()\n}"},
		{"single char content", "x", "x"},
		{"search longer than content", "x", "xyz"},
		{"newline only", "\n\n", "\n\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Find(tt.content, tt.oldStr)
			if r == nil {
				return // no match is a valid outcome
			}
			if r.Start < 0 || r.End < r.Start || r.End > len(tt.content) {
				t.Fatalf("unsliceable span [%d,%d) for content of %d bytes (strategy %s)",
					r.Start, r.End, len(tt.content), r.Strategy)
			}
			// The replacement the callers build must not panic.
			_ = tt.content[:r.Start] + "REPLACED" + tt.content[r.End:]
		})
	}
}
