package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path string, data map[string]any) {
	t.Helper()
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755)
	b, _ := json.Marshal(data)
	os.WriteFile(path, b, 0644)
}

func TestHierarchy_Get_DefaultValue(t *testing.T) {
	h := NewHierarchy("/nonexistent", "/nonexistent")
	val := h.GetString("missing", "fallback")
	if val != "fallback" {
		t.Errorf("expected fallback, got %q", val)
	}
}

func TestHierarchy_LayerPriority(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	projectDir := filepath.Join(dir, "project")

	// User layer: model = "sonnet"
	writeJSON(t, filepath.Join(configDir, "settings.json"), map[string]any{
		"model": "sonnet",
		"theme": "dark",
	})

	// Project layer: model = "haiku"
	writeJSON(t, filepath.Join(projectDir, ".bujicoder", "settings.json"), map[string]any{
		"model": "haiku",
	})

	h := NewHierarchy(configDir, projectDir)

	// User has higher priority than project
	if got := h.GetString("model", ""); got != "sonnet" {
		t.Errorf("model = %q, want 'sonnet' (user > project)", got)
	}

	// Theme only in user layer
	if got := h.GetString("theme", ""); got != "dark" {
		t.Errorf("theme = %q, want 'dark'", got)
	}
}

func TestHierarchy_ManagedOverridesAll(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")

	writeJSON(t, filepath.Join(configDir, "managed-settings.json"), map[string]any{
		"permissionMode": "bypassPermissions",
	})
	writeJSON(t, filepath.Join(configDir, "settings.json"), map[string]any{
		"permissionMode": "default",
	})

	h := NewHierarchy(configDir, "")

	if got := h.GetString("permissionMode", ""); got != "bypassPermissions" {
		t.Errorf("managed should override user, got %q", got)
	}
}

func TestHierarchy_Set(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	os.MkdirAll(configDir, 0755)

	h := NewHierarchy(configDir, "")
	err := h.Set("newKey", "newValue")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Verify persisted
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	var m map[string]any
	json.Unmarshal(data, &m)
	if m["newKey"] != "newValue" {
		t.Errorf("expected newValue, got %v", m["newKey"])
	}
}

func TestHierarchy_GetBool(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	writeJSON(t, filepath.Join(configDir, "settings.json"), map[string]any{
		"autoCompact": true,
	})

	h := NewHierarchy(configDir, "")
	if !h.GetBool("autoCompact", false) {
		t.Error("expected true")
	}
	if h.GetBool("missing", false) {
		t.Error("expected false for missing key")
	}
}

func TestHierarchy_Reload(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	settingsPath := filepath.Join(configDir, "settings.json")
	writeJSON(t, settingsPath, map[string]any{"val": "original"})

	h := NewHierarchy(configDir, "")
	if h.GetString("val", "") != "original" {
		t.Fatal("initial load failed")
	}

	// Modify file on disk
	writeJSON(t, settingsPath, map[string]any{"val": "updated"})
	h.Reload()

	if got := h.GetString("val", ""); got != "updated" {
		t.Errorf("after reload, val = %q, want 'updated'", got)
	}
}
