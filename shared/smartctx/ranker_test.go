package smartctx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"fix the login bug", []string{"login", "bug"}},
		{"add authentication to the API", []string{"authentication", "api"}},
		{"handleRequest function", []string{"handlerequest", "handle", "request", "function"}},
		{"", nil},
	}

	for _, tt := range tests {
		got := ExtractKeywords(tt.query)
		if tt.want == nil && got != nil {
			t.Errorf("ExtractKeywords(%q) = %v, want nil", tt.query, got)
			continue
		}
		for _, w := range tt.want {
			if !containsStr(got, w) {
				t.Errorf("ExtractKeywords(%q) = %v, missing %q", tt.query, got, w)
			}
		}
	}
}

func TestExtractKeywords_CamelCase(t *testing.T) {
	kw := ExtractKeywords("UserService")
	if !containsStr(kw, "userservice") {
		t.Errorf("should contain 'userservice': %v", kw)
	}
	if !containsStr(kw, "service") {
		t.Errorf("should contain 'service': %v", kw)
	}
}

func TestExtractKeywords_StopWords(t *testing.T) {
	kw := ExtractKeywords("the quick brown fox")
	if containsStr(kw, "the") {
		t.Error("should not contain stop word 'the'")
	}
	if !containsStr(kw, "quick") {
		t.Errorf("should contain 'quick': %v", kw)
	}
}

func TestRankFiles_KeywordMatch(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	os.WriteFile(filepath.Join(dir, "auth.go"), []byte("package auth\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# readme\n"), 0o644)

	// Use "auth" directly since keyword matching checks if file path contains keyword.
	ranked := RankFiles(dir, "fix auth module", nil)

	// auth.go should rank higher due to keyword match.
	found := false
	for _, fr := range ranked {
		if fr.Path == "auth.go" {
			found = true
			if !containsStr(fr.Reasons, "name_match:auth") {
				t.Errorf("auth.go should have name_match reason: %v", fr.Reasons)
			}
			break
		}
	}
	if !found {
		t.Errorf("auth.go should appear in ranked results: %v", ranked)
	}
}

func TestRankFiles_SymbolMatch(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	os.WriteFile(filepath.Join(dir, "user.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "config.go"), []byte("package main\n"), 0o644)

	ranked := RankFiles(dir, "get user data", []string{"user", "config"})

	// Both should appear with symbol_match reason.
	foundUser := false
	foundConfig := false
	for _, fr := range ranked {
		if fr.Path == "user.go" {
			foundUser = true
		}
		if fr.Path == "config.go" {
			foundConfig = true
		}
	}
	if !foundUser {
		t.Error("user.go should be ranked")
	}
	if !foundConfig {
		t.Error("config.go should be ranked")
	}
}

func TestRankFiles_EmptyProject(t *testing.T) {
	ranked := RankFiles("", "query", nil)
	if ranked != nil {
		t.Error("expected nil for empty project root")
	}
}

func TestFormatRankedFiles(t *testing.T) {
	ranked := []FileRelevance{
		{Path: "auth.go", Score: 8.0, Reasons: []string{"git_changed", "name_match:auth"}},
		{Path: "user.go", Score: 4.0, Reasons: []string{"symbol_match:user"}},
	}

	result := FormatRankedFiles(ranked)
	if result == "" {
		t.Error("FormatRankedFiles should return non-empty string")
	}
	if !strings.Contains(result, "[8.0] auth.go") {
		t.Errorf("should contain scored auth.go: %s", result)
	}
	if !strings.Contains(result, "[4.0] user.go") {
		t.Errorf("should contain scored user.go: %s", result)
	}
}

func TestFormatRankedFiles_Empty(t *testing.T) {
	result := FormatRankedFiles(nil)
	if result != "" {
		t.Errorf("expected empty string for nil, got %q", result)
	}
}

func TestRankFiles_SortedByScore(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create files with different relevance signals.
	os.WriteFile(filepath.Join(dir, "login.go"), []byte("package login\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "util.go"), []byte("package util\n"), 0o644)

	ranked := RankFiles(dir, "login system", []string{"login"})

	if len(ranked) == 0 {
		t.Fatal("expected at least one ranked file")
	}

	// First result should be login.go (keyword + symbol match).
	if ranked[0].Path != "login.go" {
		t.Errorf("expected login.go first, got %s", ranked[0].Path)
	}
}

func TestRankFiles_MaxCap(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create more than MaxRankedFiles files, all matching.
	for i := 0; i < MaxRankedFiles+20; i++ {
		name := filepath.Join(dir, strings.Repeat("a", i+1)+".go")
		os.WriteFile(name, []byte("package main\n"), 0o644)
	}

	ranked := RankFiles(dir, "aaaa", nil)
	if len(ranked) > MaxRankedFiles {
		t.Errorf("expected at most %d files, got %d", MaxRankedFiles, len(ranked))
	}
}

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input string
		want  int // expected number of parts (0 = no split)
	}{
		{"handleRequest", 2},
		{"UserService", 2},
		{"simple", 0},
		{"ABC", 0},
		{"a", 0},
	}
	for _, tt := range tests {
		parts := splitCamelCase(tt.input)
		if tt.want == 0 && parts != nil {
			t.Errorf("splitCamelCase(%q) = %v, want nil", tt.input, parts)
		} else if tt.want > 0 && len(parts) != tt.want {
			t.Errorf("splitCamelCase(%q) = %v (len %d), want %d parts", tt.input, parts, len(parts), tt.want)
		}
	}
}

// --- helpers ---

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git init: %v", err)
		}
	}
	// Create initial commit so HEAD exists.
	os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte(""), 0o644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	cmd.Run()
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if strings.Contains(v, s) {
			return true
		}
	}
	return false
}
