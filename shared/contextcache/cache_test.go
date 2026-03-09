package contextcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheGetAndRefresh(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := New(dir, 5*time.Second)

	// First read — cache miss.
	content, err := c.Get("hello.go")
	if err != nil {
		t.Fatal(err)
	}
	if content != "package main\n\nfunc main() {}\n" {
		t.Fatalf("unexpected content: %q", content)
	}

	// Second read — cache hit (should be fast).
	content2, err := c.Get("hello.go")
	if err != nil {
		t.Fatal(err)
	}
	if content2 != content {
		t.Fatal("cached content mismatch")
	}

	// Verify stats.
	count, paths := c.Stats()
	if count != 1 {
		t.Fatalf("expected 1 entry, got %d", count)
	}
	if len(paths) != 1 || paths[0] != "hello.go" {
		t.Fatalf("unexpected paths: %v", paths)
	}
}

func TestCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "a.txt")
	os.WriteFile(testFile, []byte("v1"), 0o644)

	c := New(dir, 1*time.Hour) // long TTL so we test invalidation

	c.Get("a.txt")
	count, _ := c.Stats()
	if count != 1 {
		t.Fatalf("expected 1 entry, got %d", count)
	}

	c.Invalidate("a.txt")
	count, _ = c.Stats()
	if count != 0 {
		t.Fatalf("expected 0 entries after invalidation, got %d", count)
	}
}

func TestCacheDetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "data.txt")
	os.WriteFile(testFile, []byte("original"), 0o644)

	c := New(dir, 50*time.Millisecond) // short TTL

	content, _ := c.Get("data.txt")
	if content != "original" {
		t.Fatalf("expected original, got %q", content)
	}

	// Wait for TTL to expire, then modify the file.
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(testFile, []byte("updated"), 0o644)

	content, _ = c.Get("data.txt")
	if content != "updated" {
		t.Fatalf("expected updated after TTL, got %q", content)
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := map[string]string{
		"main.go":       "go",
		"app.py":        "python",
		"index.ts":      "typescript",
		"index.tsx":     "typescript",
		"script.js":     "javascript",
		"README.md":     "markdown",
		"config.yaml":   "yaml",
		"data.json":     "json",
		"Makefile":      "",
		"query.sql":     "sql",
		"schema.proto":  "protobuf",
		"install.sh":    "shell",
		"Main.java":     "java",
		"lib.rs":        "rust",
	}

	for path, want := range tests {
		got := detectLanguage(path)
		if got != want {
			t.Errorf("detectLanguage(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestExtractGoImports(t *testing.T) {
	src := `package main

import (
	"fmt"
	"os"

	"github.com/example/pkg"
)

func main() {}
`
	imports := extractGoImports(src)
	if len(imports) != 3 {
		t.Fatalf("expected 3 imports, got %d: %v", len(imports), imports)
	}
	want := []string{"fmt", "os", "github.com/example/pkg"}
	for i, w := range want {
		if imports[i] != w {
			t.Errorf("import[%d] = %q, want %q", i, imports[i], w)
		}
	}
}

func TestExtractPythonImports(t *testing.T) {
	src := `import os
from pathlib import Path
import json
`
	imports := extractPythonImports(src)
	if len(imports) != 3 {
		t.Fatalf("expected 3 imports, got %d: %v", len(imports), imports)
	}
	want := []string{"os", "pathlib", "json"}
	for i, w := range want {
		if imports[i] != w {
			t.Errorf("import[%d] = %q, want %q", i, imports[i], w)
		}
	}
}

func TestExtractJSImports(t *testing.T) {
	src := `import React from "react";
import { useState } from 'react';
const fs = require("fs");
`
	imports := extractJSImports(src)
	if len(imports) != 3 {
		t.Fatalf("expected 3 imports, got %d: %v", len(imports), imports)
	}
	want := []string{"react", "react", "fs"}
	for i, w := range want {
		if imports[i] != w {
			t.Errorf("import[%d] = %q, want %q", i, imports[i], w)
		}
	}
}

func TestPrefetch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0o644)

	c := New(dir)
	c.Prefetch([]string{"a.go", "b.go"})

	count, _ := c.Stats()
	if count != 2 {
		t.Fatalf("expected 2 cached entries, got %d", count)
	}
}
