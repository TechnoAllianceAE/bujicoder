package lsp

import (
	"testing"
)

func TestDetectServer_Go(t *testing.T) {
	cfg, ok := DetectServer("main.go")
	if !ok {
		t.Skip("gopls not installed, skipping")
	}
	if cfg.Command != "gopls" {
		t.Errorf("Command = %q, want gopls", cfg.Command)
	}
}

func TestDetectServer_Unknown(t *testing.T) {
	_, ok := DetectServer("data.csv")
	if ok {
		t.Error("should not detect server for .csv")
	}
}

func TestDetectServer_NotInstalled(t *testing.T) {
	// Save and restore original
	orig := languageServers[".xyz"]
	languageServers[".xyz"] = struct {
		command string
		args    []string
		langID  string
	}{"nonexistent-lsp-binary-xyz123", nil, "xyz"}
	defer func() {
		if orig.command == "" {
			delete(languageServers, ".xyz")
		} else {
			languageServers[".xyz"] = orig
		}
	}()

	_, ok := DetectServer("test.xyz")
	if ok {
		t.Error("should not detect server when binary not installed")
	}
}

func TestLanguageID(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{"main.go", "go"},
		{"app.ts", "typescript"},
		{"style.css", "plaintext"},
		{"script.py", "python"},
		{"lib.rs", "rust"},
	}
	for _, tt := range tests {
		got := LanguageID(tt.file)
		if got != tt.want {
			t.Errorf("LanguageID(%q) = %q, want %q", tt.file, got, tt.want)
		}
	}
}

func TestFormatDiagnostics_Empty(t *testing.T) {
	result := FormatDiagnostics(nil, 10)
	if result != "" {
		t.Errorf("expected empty string for nil diagnostics, got %q", result)
	}
}

func TestFormatDiagnostics_WithErrors(t *testing.T) {
	diags := []Diagnostic{
		{File: "main.go", Line: 10, Column: 5, Severity: "error", Message: "undefined: foo"},
		{File: "main.go", Line: 20, Column: 1, Severity: "error", Message: "syntax error"},
	}

	result := FormatDiagnostics(diags, 10)
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	if !contains(result, "main.go:10:5: undefined: foo") {
		t.Errorf("result should contain first error, got:\n%s", result)
	}
	if !contains(result, "main.go:20:1: syntax error") {
		t.Errorf("result should contain second error, got:\n%s", result)
	}
}

func TestFormatDiagnostics_MaxCap(t *testing.T) {
	var diags []Diagnostic
	for i := 0; i < 15; i++ {
		diags = append(diags, Diagnostic{
			File:    "main.go",
			Line:    i + 1,
			Column:  1,
			Message: "error",
		})
	}

	result := FormatDiagnostics(diags, 5)
	if !contains(result, "... and 10 more errors") {
		t.Errorf("should cap at 5 and show remainder, got:\n%s", result)
	}
}

func TestNewManager(t *testing.T) {
	mgr := NewManager("/tmp")
	if mgr == nil {
		t.Fatal("NewManager should not return nil")
	}
	mgr.CloseAll()
}

func TestManager_DiagnoseNoServer(t *testing.T) {
	mgr := NewManager("/tmp")
	defer mgr.CloseAll()

	// .csv has no language server — should return nil silently.
	diags := mgr.Diagnose("data.csv", "some content")
	if diags != nil {
		t.Errorf("expected nil for unknown file type, got %v", diags)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
