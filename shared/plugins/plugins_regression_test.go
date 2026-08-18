package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// An unreadable command file must be skipped, not registered as an empty
// command that silently does nothing when invoked.
func TestLoadSkipsUnreadableCommandFiles(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	setupPlugin(t, pluginsDir, "p", Plugin{Name: "p", Version: "1"}, map[string]string{
		"good": "usable content",
	})

	// Add a command file that cannot be read.
	bad := filepath.Join(pluginsDir, "p", "commands", "bad.md")
	if err := os.WriteFile(bad, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Skipf("cannot make file unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })
	if _, err := os.ReadFile(bad); err == nil {
		t.Skip("file permissions are not enforced here")
	}

	pm := &Manager{plugins: make(map[string]*Plugin)}
	pm.loadFromDir(pluginsDir)

	p := pm.GetPlugin("p")
	if p == nil {
		t.Fatal("plugin not loaded")
	}
	for _, c := range p.Commands {
		if c.Name == "bad" {
			t.Fatal("unreadable command was registered with empty content")
		}
		if c.Content == "" {
			t.Fatalf("command %q registered with empty content", c.Name)
		}
	}
	if len(p.Commands) != 1 {
		t.Fatalf("expected only the readable command, got %d", len(p.Commands))
	}
}

// Project plugins must deterministically override user plugins of the same name.
func TestProjectPluginsOverrideUserPlugins(t *testing.T) {
	configDir := t.TempDir()
	projectRoot := t.TempDir()

	setupPlugin(t, filepath.Join(configDir, "plugins"), "dup",
		Plugin{Name: "dup", Version: "user"}, nil)
	setupPlugin(t, filepath.Join(projectRoot, ".bujicoder", "plugins"), "dup",
		Plugin{Name: "dup", Version: "project"}, nil)

	for range 5 {
		pm := NewManager(configDir, projectRoot)
		p := pm.GetPlugin("dup")
		if p == nil {
			t.Fatal("plugin not loaded")
		}
		if p.Version != "project" {
			t.Fatalf("Version = %q, want the project plugin to win", p.Version)
		}
	}
}

func TestLoadIgnoresMalformedManifest(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsDir, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "broken", "plugin.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	setupPlugin(t, pluginsDir, "ok", Plugin{Name: "ok", Version: "1"}, nil)

	pm := &Manager{plugins: make(map[string]*Plugin)}
	pm.loadFromDir(pluginsDir)

	if pm.GetPlugin("broken") != nil {
		t.Fatal("malformed manifest was loaded")
	}
	if pm.GetPlugin("ok") == nil {
		t.Fatal("a malformed plugin prevented the rest from loading")
	}
}
