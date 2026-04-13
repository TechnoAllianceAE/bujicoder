package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_WriteAndList(t *testing.T) {
	dir := t.TempDir()
	s := &Store{baseDir: dir}

	err := s.Write("test-memory", "a test entry", TypeProject, "This is the content.")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	entries := s.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Name != "test-memory" {
		t.Errorf("Name = %q, want %q", e.Name, "test-memory")
	}
	if e.Type != TypeProject {
		t.Errorf("Type = %q, want %q", e.Type, TypeProject)
	}
	if e.Content != "This is the content." {
		t.Errorf("Content = %q, want %q", e.Content, "This is the content.")
	}
}

func TestStore_Delete(t *testing.T) {
	dir := t.TempDir()
	s := &Store{baseDir: dir}

	_ = s.Write("to-delete", "will be deleted", TypeFeedback, "bye")
	if len(s.List()) != 1 {
		t.Fatal("expected 1 entry after write")
	}

	err := s.Delete("to-delete")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if len(s.List()) != 0 {
		t.Error("expected 0 entries after delete")
	}
}

func TestStore_GetPrompt_Empty(t *testing.T) {
	s := &Store{baseDir: "/nonexistent/path"}
	prompt := s.GetPrompt()
	if prompt != "" {
		t.Errorf("expected empty prompt, got %q", prompt)
	}
}

func TestStore_GetPrompt_WithEntries(t *testing.T) {
	dir := t.TempDir()
	s := &Store{baseDir: dir}

	_ = s.Write("my-mem", "desc", TypeUser, "content here")

	prompt := s.GetPrompt()
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !containsStr(prompt, "Persistent Memories") {
		t.Error("prompt should contain header")
	}
	if !containsStr(prompt, "my-mem") {
		t.Error("prompt should contain memory name")
	}
	if !containsStr(prompt, "content here") {
		t.Error("prompt should contain memory content")
	}
}

func TestParseMemoryFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.md")
	content := "---\nname: Test\ndescription: A test\ntype: feedback\n---\n\nBody text here."
	os.WriteFile(fp, []byte(content), 0644)

	entry := parseMemoryFile(fp)
	if entry == nil {
		t.Fatal("parseMemoryFile returned nil")
	}
	if entry.Name != "Test" {
		t.Errorf("Name = %q, want %q", entry.Name, "Test")
	}
	if entry.Description != "A test" {
		t.Errorf("Description = %q, want %q", entry.Description, "A test")
	}
	if entry.Type != TypeFeedback {
		t.Errorf("Type = %q, want %q", entry.Type, TypeFeedback)
	}
	if entry.Content != "Body text here." {
		t.Errorf("Content = %q, want %q", entry.Content, "Body text here.")
	}
}

func TestStore_SkipsMemoryIndex(t *testing.T) {
	dir := t.TempDir()
	s := &Store{baseDir: dir}

	_ = os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("index"), 0644)
	_ = s.Write("real", "real entry", TypeProject, "content")

	entries := s.List()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (MEMORY.md excluded), got %d", len(entries))
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
