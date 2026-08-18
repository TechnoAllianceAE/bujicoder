package smartctx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractKeywordsSplitsCamelCase checks the documented camelCase split
// happens. The word was lowercased before splitting, so no case boundary was
// left and the split silently never produced anything.
func TestExtractKeywordsSplitsCamelCase(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"camel", "fix handleRequest", []string{"handlerequest", "handle", "request"}},
		{"pascal", "the UserService is broken", []string{"userservice", "user", "service"}},
		{"multi", "refactor parseHTTPResponse now", []string{"parsehttpresponse", "parse"}},
		{"already lower", "lookup token", []string{"lookup", "token"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractKeywords(tc.query)
			for _, want := range tc.want {
				if !contains(got, want) {
					t.Errorf("ExtractKeywords(%q) = %v, missing %q", tc.query, got, want)
				}
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestExtractKeywordsIsBounded checks an oversized query cannot blow up ranking,
// which does a substring test per keyword per project file.
func TestExtractKeywordsIsBounded(t *testing.T) {
	var sb strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&sb, "identifier%d ", i)
	}

	got := ExtractKeywords(sb.String())
	if len(got) > MaxKeywords {
		t.Errorf("ExtractKeywords returned %d keywords, want at most %d", len(got), MaxKeywords)
	}
	if len(got) == 0 {
		t.Error("ExtractKeywords returned nothing for a long query")
	}
}

// TestRankFilesHandlesMissingRepoAndEmptyQuery covers the degenerate inputs the
// runtime can hand it.
func TestRankFilesHandlesMissingRepoAndEmptyQuery(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		query   string
		symbols []string
	}{
		{"empty root", "", "anything", nil},
		{"missing dir", filepath.Join(t.TempDir(), "does-not-exist"), "anything", nil},
		{"empty query", t.TempDir(), "", nil},
		{"nil symbols", t.TempDir(), "query", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A panic here fails the test; that is the assertion.
			got := RankFiles(tc.root, tc.query, tc.symbols)
			if len(got) > MaxRankedFiles {
				t.Errorf("returned %d files, want at most %d", len(got), MaxRankedFiles)
			}
		})
	}
}

// TestRankFilesIgnoresHugeChangedFile checks that a large changed file is not
// read whole just to look for imports.
func TestRankFilesIgnoresHugeChangedFile(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// A sparse file above the import-scan bound.
	huge := filepath.Join(dir, "bundle.js")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(MaxImportScanBytes + 1); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := extractImportPaths(dir, "bundle.js"); got != nil {
		t.Errorf("extractImportPaths read an oversized file: %v", got)
	}

	// A normal-sized file is still scanned.
	small := "import x from './dep';\n"
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte(small), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dep.js"), []byte("export default 1;\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := extractImportPaths(dir, "app.js")
	if !contains(got, "dep.js") {
		t.Errorf("extractImportPaths(app.js) = %v, want to include dep.js", got)
	}
}

func TestExtractImportPathsHandlesMissingAndDirectory(t *testing.T) {
	dir := t.TempDir()
	if got := extractImportPaths(dir, "missing.js"); got != nil {
		t.Errorf("missing file returned %v", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "adir.js"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := extractImportPaths(dir, "adir.js"); got != nil {
		t.Errorf("directory returned %v", got)
	}
}

// TestRankFilesBoundsScan checks that ranking a large tree stays bounded.
func TestRankFilesBoundsScan(t *testing.T) {
	dir := t.TempDir()
	for i := range 200 {
		name := filepath.Join(dir, fmt.Sprintf("handler%03d.go", i))
		if err := os.WriteFile(name, []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	got := RankFiles(dir, "handler", nil)
	if len(got) > MaxRankedFiles {
		t.Errorf("RankFiles returned %d files, want at most %d", len(got), MaxRankedFiles)
	}
	if len(got) == 0 {
		t.Error("RankFiles matched nothing for a name that is present")
	}
}

// TestRankFilesSurvivesUnreadableDirectory checks that one unreadable directory
// does not abort ranking of the rest of the tree.
func TestRankFilesSurvivesUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions are not enforced")
	}
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locked, "inner.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got := RankFiles(dir, "handler", nil)
	found := false
	for _, fr := range got {
		if fr.Path == "handler.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("handler.go missing from ranking: %+v", got)
	}
}

func TestSplitCamelCaseParts(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"handleRequest", []string{"handle", "Request"}},
		{"UserService", []string{"User", "Service"}},
		{"lowercase", nil},
		{"a", nil},
		{"", nil},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := splitCamelCase(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitCamelCase(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("part %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
