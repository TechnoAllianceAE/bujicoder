package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Initialize a real git repo so ensureGitignore works.
	m := &Manager{projectRoot: dir, snapshotDir: filepath.Join(dir, ".bujicoder", "snapshots")}
	// Pre-create a file in the project.
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	_ = m // just for setup

	return dir
}

func TestNewManager(t *testing.T) {
	dir := setupTestProject(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Snapshot dir should exist with .git inside.
	gitDir := filepath.Join(dir, ".bujicoder", "snapshots", ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Error("snapshot .git dir should exist")
	}
	_ = mgr
}

func TestTakeAndList(t *testing.T) {
	dir := setupTestProject(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Write a file to snapshot.
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644)

	snap, err := mgr.Take(1, "editor", "write_file", []string{"hello.txt"})
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if snap == nil {
		t.Fatal("snapshot should not be nil")
	}
	if snap.StepNum != 1 {
		t.Errorf("StepNum = %d, want 1", snap.StepNum)
	}
	if snap.AgentID != "editor" {
		t.Errorf("AgentID = %q, want editor", snap.AgentID)
	}
	if snap.ID == "" {
		t.Error("snapshot ID should not be empty")
	}

	// List should return the snapshot.
	snaps, err := mgr.List(10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// At least 1 (could be 2 including init commit, but init has no step metadata).
	found := false
	for _, s := range snaps {
		if s.StepNum == 1 && s.AgentID == "editor" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find step 1 snapshot in list")
	}
}

func TestTakeNoChanges(t *testing.T) {
	dir := setupTestProject(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Take with a non-existent file — should return nil (no changes).
	snap, err := mgr.Take(1, "editor", "write_file", []string{"nonexistent.txt"})
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if snap != nil {
		t.Error("snapshot should be nil when no changes")
	}
}

func TestRevert(t *testing.T) {
	dir := setupTestProject(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Step 1: write hello.txt = "version 1"
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("version 1\n"), 0o644)
	snap1, err := mgr.Take(1, "editor", "write_file", []string{"hello.txt"})
	if err != nil || snap1 == nil {
		t.Fatalf("Take step 1: snap=%v err=%v", snap1, err)
	}

	// Step 2: write hello.txt = "version 2"
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("version 2\n"), 0o644)
	_, err = mgr.Take(2, "editor", "str_replace", []string{"hello.txt"})
	if err != nil {
		t.Fatalf("Take step 2: %v", err)
	}

	// Verify current state.
	data, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if string(data) != "version 2\n" {
		t.Fatalf("expected version 2, got %q", data)
	}

	// Revert to step 1.
	if err := mgr.Revert(snap1.ID); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	// File should be back to version 1.
	data, _ = os.ReadFile(filepath.Join(dir, "hello.txt"))
	if string(data) != "version 1\n" {
		t.Errorf("after revert, got %q, want 'version 1\\n'", data)
	}
}

func TestDiff(t *testing.T) {
	dir := setupTestProject(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa\n"), 0o644)
	snap1, _ := mgr.Take(1, "editor", "write_file", []string{"a.txt"})

	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("bbb\n"), 0o644)
	snap2, _ := mgr.Take(2, "editor", "str_replace", []string{"a.txt"})

	if snap1 == nil || snap2 == nil {
		t.Fatal("both snapshots should be non-nil")
	}

	diff, err := mgr.Diff(snap1.ID, snap2.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff == "" {
		t.Error("diff should not be empty")
	}
	if !contains(diff, "-aaa") || !contains(diff, "+bbb") {
		t.Errorf("diff should show aaa→bbb change, got:\n%s", diff)
	}
}

func TestMultipleFiles(t *testing.T) {
	dir := setupTestProject(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	os.WriteFile(filepath.Join(dir, "file1.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "file2.go"), []byte("package b\n"), 0o644)

	snap, err := mgr.Take(1, "base", "write_file", []string{"file1.go", "file2.go"})
	if err != nil || snap == nil {
		t.Fatalf("Take: snap=%v err=%v", snap, err)
	}
	if len(snap.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(snap.Files))
	}
}

func TestEnsureGitignore(t *testing.T) {
	dir := setupTestProject(t)
	_, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !contains(string(data), ".bujicoder/") {
		t.Error(".gitignore should contain .bujicoder/")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsHelper(s, substr)
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
