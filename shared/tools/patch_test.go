package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultiEdit_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\n\nfunc hello() {}\nfunc world() {}\n"), 0o644)

	fn := multiEdit(dir, &ProjectPermissions{})
	args := `{
		"edits": [
			{"path": "test.go", "old_str": "func hello() {}", "new_str": "func hello() { return }"},
			{"path": "test.go", "old_str": "func world() {}", "new_str": "func world() { return }"}
		]
	}`

	result, err := fn(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("multiEdit: %v", err)
	}
	if !strings.Contains(result, "2/2 edits applied") {
		t.Errorf("expected 2/2 applied, got:\n%s", result)
	}

	// Verify file content.
	data, _ := os.ReadFile(filepath.Join(dir, "test.go"))
	if !strings.Contains(string(data), "func hello() { return }") {
		t.Error("first edit not applied")
	}
	if !strings.Contains(string(data), "func world() { return }") {
		t.Error("second edit not applied")
	}
}

func TestMultiEdit_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\n"), 0o644)

	fn := multiEdit(dir, &ProjectPermissions{})
	args := `{
		"edits": [
			{"path": "test.go", "old_str": "package main", "new_str": "package app"},
			{"path": "test.go", "old_str": "NONEXISTENT", "new_str": "REPLACEMENT"}
		]
	}`

	result, err := fn(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("multiEdit: %v", err)
	}
	if !strings.Contains(result, "1 applied") {
		t.Errorf("expected 1 applied, got:\n%s", result)
	}
}

func TestMultiEdit_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644)

	fn := multiEdit(dir, &ProjectPermissions{})
	args := `{
		"edits": [
			{"path": "a.go", "old_str": "package a", "new_str": "package alpha"},
			{"path": "b.go", "old_str": "package b", "new_str": "package beta"}
		]
	}`

	result, err := fn(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("multiEdit: %v", err)
	}
	if !strings.Contains(result, "2 applied") {
		t.Errorf("expected 2 applied, got:\n%s", result)
	}

	dataA, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(dataA), "package alpha") {
		t.Error("a.go not updated")
	}
	dataB, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(dataB), "package beta") {
		t.Error("b.go not updated")
	}
}

func TestParsePatch_NewFile(t *testing.T) {
	patch := `--- /dev/null
+++ b/new_file.go
@@ -0,0 +1,3 @@
+package main
+
+func New() {}
`
	ops, err := parsePatch(patch)
	if err != nil {
		t.Fatalf("parsePatch: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Action != "add" {
		t.Errorf("Action = %q, want add", ops[0].Action)
	}
	if ops[0].Path != "new_file.go" {
		t.Errorf("Path = %q, want new_file.go", ops[0].Path)
	}
	if !strings.Contains(ops[0].Content, "package main") {
		t.Error("content should contain package main")
	}
}

func TestParsePatch_DeleteFile(t *testing.T) {
	patch := `--- a/old_file.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package old
-
-func Old() {}
`
	ops, err := parsePatch(patch)
	if err != nil {
		t.Fatalf("parsePatch: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Action != "delete" {
		t.Errorf("Action = %q, want delete", ops[0].Action)
	}
	if ops[0].Path != "old_file.go" {
		t.Errorf("Path = %q, want old_file.go", ops[0].Path)
	}
}

func TestParsePatch_UpdateFile(t *testing.T) {
	patch := `--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 package main

-func old() {}
+func updated() {}
`
	ops, err := parsePatch(patch)
	if err != nil {
		t.Fatalf("parsePatch: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Action != "update" {
		t.Errorf("Action = %q, want update", ops[0].Action)
	}
}

func TestApplyPatch_AddFile(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())

	fn := applyPatch(dir, &ProjectPermissions{})
	patch := `--- /dev/null
+++ b/created.txt
@@ -0,0 +1 @@
+hello world
`
	args, _ := json.Marshal(map[string]string{"patch": patch})

	result, err := fn(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("applyPatch: %v", err)
	}
	if !strings.Contains(result, "ADD created.txt") {
		t.Errorf("expected ADD, got:\n%s", result)
	}

	data, err := os.ReadFile(filepath.Join(dir, "created.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if !strings.Contains(string(data), "hello world") {
		t.Errorf("file content wrong: %q", data)
	}
}

func TestApplyPatch_DeleteFile(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(dir, "todelete.txt"), []byte("remove me"), 0o644)

	fn := applyPatch(dir, &ProjectPermissions{})
	patch := `--- a/todelete.txt
+++ /dev/null
@@ -1 +0,0 @@
-remove me
`
	args, _ := json.Marshal(map[string]string{"patch": patch})

	result, err := fn(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("applyPatch: %v", err)
	}
	if !strings.Contains(result, "DELETE todelete.txt") {
		t.Errorf("expected DELETE, got:\n%s", result)
	}

	if _, err := os.Stat(filepath.Join(dir, "todelete.txt")); !os.IsNotExist(err) {
		t.Error("file should have been deleted")
	}
}

func TestParseDiffPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"--- a/src/main.go", "src/main.go"},
		{"+++ b/src/main.go", "src/main.go"},
		{"--- /dev/null", ""},
		{"+++ /dev/null", ""},
	}
	for _, tt := range tests {
		got := parseDiffPath(tt.input)
		if got != tt.want {
			t.Errorf("parseDiffPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
