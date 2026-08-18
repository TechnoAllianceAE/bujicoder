package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A malformed settings.json must not be silently replaced by a one-key file:
// that destroys everything the user had configured.
func TestSetRefusesToClobberMalformedFile(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "settings.json")
	original := `{"theme": "dark", oops`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHierarchy(configDir, "")
	err := h.Set("model", "gpt-5")
	if err == nil {
		t.Fatal("Set must fail when the existing settings file cannot be parsed")
	}
	if !strings.Contains(err.Error(), "could not be parsed") {
		t.Fatalf("unexpected error: %v", err)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("the malformed file was overwritten:\n%s", got)
	}
}

// After the file is repaired, Set works again.
func TestSetWritesAfterMalformedFileIsFixed(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "settings.json")
	if err := os.WriteFile(path, []byte("{ broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHierarchy(configDir, "")
	if err := h.Set("k", "v"); err == nil {
		t.Fatal("expected refusal")
	}

	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Reload()
	if err := h.Set("model", "gpt-5"); err != nil {
		t.Fatalf("Set after repair: %v", err)
	}
	if got := h.GetString("theme", ""); got != "dark" {
		t.Fatalf("existing key lost: theme=%q", got)
	}
	if got := h.GetString("model", ""); got != "gpt-5" {
		t.Fatalf("new key not stored: model=%q", got)
	}
}

// A write with no matching layer must report an error, not pretend to succeed.
func TestSetProjectWithoutProjectLayerReportsError(t *testing.T) {
	h := NewHierarchy(t.TempDir(), "")
	if err := h.SetProject("k", "v"); err == nil {
		t.Fatal("SetProject silently succeeded without a project layer")
	}
}

// The settings file must be replaced atomically and leave no temp files behind.
func TestSaveLayerIsAtomic(t *testing.T) {
	configDir := t.TempDir()
	h := NewHierarchy(configDir, "")
	if err := h.Set("model", "gpt-5"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected only settings.json, found %v", names)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("persisted settings are not valid JSON: %v", err)
	}
	if parsed["model"] != "gpt-5" {
		t.Fatalf("persisted settings = %v", parsed)
	}

	info, err := os.Stat(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("settings.json mode = %v, want 0644", perm)
	}
}

// Layer precedence must be deterministic: managed > user > local > project.
func TestLayerPrecedenceIsDeterministic(t *testing.T) {
	configDir := t.TempDir()
	projectRoot := t.TempDir()
	projectCfg := filepath.Join(projectRoot, ".bujicoder")
	if err := os.MkdirAll(projectCfg, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(path string, content map[string]string) {
		data, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(configDir, "managed-settings.json"), map[string]string{"a": "managed"})
	write(filepath.Join(configDir, "settings.json"), map[string]string{"a": "user", "b": "user"})
	write(filepath.Join(projectCfg, "settings.local.json"), map[string]string{"a": "local", "b": "local", "c": "local"})
	write(filepath.Join(projectCfg, "settings.json"), map[string]string{"a": "project", "b": "project", "c": "project", "d": "project"})

	h := NewHierarchy(configDir, projectRoot)
	for _, tc := range []struct{ key, want string }{
		{"a", "managed"},
		{"b", "user"},
		{"c", "local"},
		{"d", "project"},
	} {
		if got := h.GetString(tc.key, ""); got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
	if names := strings.Join(h.LayerNames(), ","); names != "managed,user,local,project" {
		t.Fatalf("layer order = %s", names)
	}
}
