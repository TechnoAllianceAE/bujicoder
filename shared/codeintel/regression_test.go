package codeintel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestExtractSymbolsSurvivesHostileInput is the crash contract: the parsers run
// on whatever is in the user's repository, so no input may panic.
func TestExtractSymbolsSurvivesHostileInput(t *testing.T) {
	p := NewParser()

	inputs := map[string]string{
		"empty":              "",
		"only newlines":      "\n\n\n",
		"nul bytes":          "\x00\x00\x00package main\x00",
		"invalid utf8":       "package main\n\xff\xfe\xfd func Broken(\n",
		"unterminated brace": "func main() {\n\tif true {\n",
		"unterminated paren": "func main(a, b\n",
		"lone cr":            "package main\rfunc a() {}\r",
		"huge single line":   "func " + strings.Repeat("a", 200000) + "() {",
		"deep nesting":       strings.Repeat("{", 5000) + strings.Repeat("}", 5000),
		"only closing brace": "}}}}\n",
		"bom":                "\ufeffpackage main\nfunc A() {}\n",
		"binary":             string([]byte{0x7f, 0x45, 0x4c, 0x46, 0x02, 0x01, 0x01, 0x00, 0x00}),
	}

	exts := []string{".go", ".py", ".ts", ".js", ".rs", ".tsx", ".jsx"}

	for name, content := range inputs {
		for _, ext := range exts {
			t.Run(name+ext, func(t *testing.T) {
				// A panic here fails the test; that is the assertion.
				syms := p.ExtractSymbols("file"+ext, []byte(content))
				for _, s := range syms {
					if s.StartLine < 1 {
						t.Errorf("symbol %q has StartLine %d", s.Name, s.StartLine)
					}
					if s.EndLine < s.StartLine {
						t.Errorf("symbol %q has EndLine %d before StartLine %d", s.Name, s.EndLine, s.StartLine)
					}
					if !utf8.ValidString(s.Signature) && utf8.ValidString(content) {
						t.Errorf("symbol %q signature is not valid UTF-8: %q", s.Name, s.Signature)
					}
				}
			})
		}
	}
}

// TestSignatureTruncationIsRuneSafe checks a long multi-byte declaration line is
// not cut mid-rune, which produced invalid UTF-8 in the prompt.
func TestSignatureTruncationIsRuneSafe(t *testing.T) {
	p := NewParser()

	src := "func Handle(パラメータ " + strings.Repeat("日", 300) + " string) {\n}\n"
	syms := p.ExtractSymbols("x.go", []byte(src))
	if len(syms) == 0 {
		t.Skip("no symbols extracted from the fixture")
	}
	for _, s := range syms {
		if !utf8.ValidString(s.Signature) {
			t.Errorf("signature is not valid UTF-8: %q", s.Signature)
		}
	}

	// The regex fallback path builds the signature via truncateLine.
	fallback := extractGoSymbolsRegex(src)
	for _, s := range fallback {
		if !utf8.ValidString(s.Signature) {
			t.Errorf("fallback signature is not valid UTF-8: %q", s.Signature)
		}
	}
}

func TestTruncateLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short", "abc", 10, "abc"},
		{"exact bytes", "abcde", 5, "abcde"},
		{"cut ascii", "abcdefgh", 6, "abc..."},
		{"cut multibyte", strings.Repeat("日", 10), 6, "日日日..."},
		// maxLen-3 is not a rune multiple: byte slicing splits a rune here.
		{"cut mid rune", strings.Repeat("日", 10), 7, "日日日日..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateLine(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateLine(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result is not valid UTF-8: %q", got)
			}
		})
	}
}

// TestIndentAwarePatternsClassifyMethods checks the indentation-anchored
// patterns actually apply. They were matched against a trimmed line, so they
// could never fire and every method was reported as a free function.
func TestIndentAwarePatternsClassifyMethods(t *testing.T) {
	p := NewParser()

	t.Run("python", func(t *testing.T) {
		src := "class Service:\n    def handle(self, req):\n        return 1\n\ndef free_fn():\n    pass\n"
		kinds := map[string]string{}
		for _, s := range p.ExtractSymbols("s.py", []byte(src)) {
			kinds[s.Name] = s.Kind
		}
		if kinds["Service"] != "class" {
			t.Errorf("Service kind = %q, want class", kinds["Service"])
		}
		if kinds["handle"] != "method" {
			t.Errorf("handle kind = %q, want method", kinds["handle"])
		}
		if kinds["free_fn"] != "function" {
			t.Errorf("free_fn kind = %q, want function", kinds["free_fn"])
		}
	})

	t.Run("rust", func(t *testing.T) {
		src := "pub struct S;\n\nimpl S {\n    pub fn method(&self) {}\n}\n\npub fn free_fn() {}\n"
		kinds := map[string]string{}
		for _, s := range p.ExtractSymbols("s.rs", []byte(src)) {
			kinds[s.Name] = s.Kind
		}
		if kinds["method"] != "method" {
			t.Errorf("method kind = %q, want method", kinds["method"])
		}
		if kinds["free_fn"] != "function" {
			t.Errorf("free_fn kind = %q, want function", kinds["free_fn"])
		}
	})
}

// TestExtractSymbolsFromFileRejectsHugeFile checks that indexing a repository
// containing a huge generated source does not read it into memory.
func TestExtractSymbolsFromFileRejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "generated.go")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Sparse-friendly: extend the file without writing its content.
	if err := f.Truncate(MaxSourceFileSize + 1); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	p := NewParser()
	if _, err := p.ExtractSymbolsFromFile(path); err == nil {
		t.Fatal("ExtractSymbolsFromFile accepted an oversized file")
	}
}

func TestExtractSymbolsFromFileRejectsDirectory(t *testing.T) {
	p := NewParser()
	if _, err := p.ExtractSymbolsFromFile(t.TempDir()); err == nil {
		t.Fatal("ExtractSymbolsFromFile accepted a directory")
	}
}

// TestIndexProjectSkipsUnreadableFilesAndContinues checks a single bad file does
// not abort the whole scan.
func TestIndexProjectSkipsUnreadableFilesAndContinues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions are not enforced")
	}
	dir := t.TempDir()

	good := filepath.Join(dir, "good.go")
	if err := os.WriteFile(good, []byte("package main\n\nfunc Good() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	bad := filepath.Join(dir, "bad.go")
	if err := os.WriteFile(bad, []byte("package main\n\nfunc Bad() {}\n"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	p := NewParser()
	index := p.IndexProject(dir, nil)

	found := false
	for _, fs := range index {
		if fs.Path == "good.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("good.go missing from index: %+v", index)
	}
}

// TestIndexProjectHonoursFileCap keeps whole-repo indexing bounded.
func TestIndexProjectHonoursFileCap(t *testing.T) {
	dir := t.TempDir()
	for i := range 150 {
		name := filepath.Join(dir, fmt.Sprintf("f%03d.go", i))
		body := fmt.Sprintf("package main\n\nfunc F%03d() {}\n", i)
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	p := NewParser()
	index := p.IndexProject(dir, nil)
	if len(index) > 100 {
		t.Errorf("indexed %d files, want at most 100", len(index))
	}
	if len(index) == 0 {
		t.Error("indexed no files")
	}
}

// TestIndexFilesHandlesMissingAndUnsupportedPaths covers the explicit-file path.
func TestIndexFilesHandlesMissingAndUnsupportedPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	p := NewParser()
	index := p.IndexProject(dir, []string{"a.go", "missing.go", "notes.txt", "", ".."})
	if len(index) != 1 {
		t.Fatalf("indexed %d files, want 1: %+v", len(index), index)
	}
	if index[0].Path != "a.go" {
		t.Errorf("Path = %q, want a.go", index[0].Path)
	}
}
