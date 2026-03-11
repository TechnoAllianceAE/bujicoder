package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew_CreatesLogFile(t *testing.T) {
	dir := t.TempDir()
	log := New(Config{Dir: dir, Level: "info"})

	log.Info().Msg("test message")

	logFile := filepath.Join(dir, "bujicoder.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("expected log file to exist: %v", err)
	}
	if !strings.Contains(string(data), "test message") {
		t.Fatalf("log file should contain 'test message', got: %s", string(data))
	}
}

func TestNew_JSONFormat(t *testing.T) {
	dir := t.TempDir()
	log := New(Config{Dir: dir, Level: "debug"})

	log.Error().Str("provider", "openrouter").Str("model", "test/model").Msg("LLM failed")

	data, err := os.ReadFile(filepath.Join(dir, "bujicoder.log"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `"provider":"openrouter"`) {
		t.Fatalf("expected structured provider field, got: %s", content)
	}
	if !strings.Contains(content, `"model":"test/model"`) {
		t.Fatalf("expected structured model field, got: %s", content)
	}
	if !strings.Contains(content, `"level":"error"`) {
		t.Fatalf("expected error level, got: %s", content)
	}
}

func TestNew_LevelFiltering(t *testing.T) {
	dir := t.TempDir()
	log := New(Config{Dir: dir, Level: "error"})

	log.Info().Msg("should be filtered")
	log.Error().Msg("should appear")

	data, err := os.ReadFile(filepath.Join(dir, "bujicoder.log"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "should be filtered") {
		t.Fatal("info message should have been filtered at error level")
	}
	if !strings.Contains(content, "should appear") {
		t.Fatal("error message should appear")
	}
}

func TestNew_IncludesVersion(t *testing.T) {
	dir := t.TempDir()
	log := New(Config{Dir: dir, Level: "info"})

	log.Info().Msg("version check")

	data, err := os.ReadFile(filepath.Join(dir, "bujicoder.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version"`) {
		t.Fatal("log entries should include version field")
	}
}

func TestNew_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	altDir := filepath.Join(dir, "custom")

	t.Setenv("BUJICODER_LOG_DIR", altDir)
	t.Setenv("BUJICODER_LOG_LEVEL", "debug")

	log := New(Config{Dir: dir}) // dir should be overridden by env var

	log.Debug().Msg("env override test")

	data, err := os.ReadFile(filepath.Join(altDir, "bujicoder.log"))
	if err != nil {
		t.Fatalf("expected log in env-overridden dir: %v", err)
	}
	if !strings.Contains(string(data), "env override test") {
		t.Fatal("debug message should appear when env level is debug")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"debug", "debug"},
		{"info", "info"},
		{"warn", "warn"},
		{"warning", "warn"},
		{"error", "error"},
		{"TRACE", "trace"},
		{"banana", "info"}, // default
	}
	for _, tt := range tests {
		got := parseLevel(tt.input)
		if got.String() != tt.expected {
			t.Errorf("parseLevel(%q) = %s, want %s", tt.input, got, tt.expected)
		}
	}
}

func TestLogDir(t *testing.T) {
	dir := LogDir(Config{Dir: "/custom/path"})
	if dir != "/custom/path" {
		t.Fatalf("expected /custom/path, got %s", dir)
	}
}
