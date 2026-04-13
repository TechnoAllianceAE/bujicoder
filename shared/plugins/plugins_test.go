package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func setupPlugin(t *testing.T, dir, name string, manifest Plugin, commands map[string]string) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	os.MkdirAll(pluginDir, 0755)

	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	if len(commands) > 0 {
		cmdDir := filepath.Join(pluginDir, "commands")
		os.MkdirAll(cmdDir, 0755)
		for cmdName, content := range commands {
			os.WriteFile(filepath.Join(cmdDir, cmdName+".md"), []byte(content), 0644)
		}
	}
}

func TestManager_LoadPlugin(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")

	setupPlugin(t, pluginsDir, "test-plugin", Plugin{
		Name:        "test-plugin",
		Version:     "1.0.0",
		Description: "A test plugin",
		Author:      "test",
	}, map[string]string{
		"hello": "Say hello to the user",
	})

	pm := &Manager{plugins: make(map[string]*Plugin)}
	pm.loadFromDir(pluginsDir)

	if len(pm.plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(pm.plugins))
	}

	p := pm.GetPlugin("test-plugin")
	if p == nil {
		t.Fatal("plugin not found")
	}
	if p.Version != "1.0.0" {
		t.Errorf("Version = %q", p.Version)
	}
	if !p.Enabled {
		t.Error("plugin should be enabled by default")
	}
	if len(p.Commands) != 1 {
		t.Errorf("expected 1 command, got %d", len(p.Commands))
	}
	if p.Commands[0].Name != "hello" {
		t.Errorf("command name = %q", p.Commands[0].Name)
	}
}

func TestManager_EnableDisable(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")

	setupPlugin(t, pluginsDir, "my-plugin", Plugin{
		Name: "my-plugin",
	}, nil)

	pm := &Manager{plugins: make(map[string]*Plugin)}
	pm.loadFromDir(pluginsDir)

	if err := pm.DisablePlugin("my-plugin"); err != nil {
		t.Fatal(err)
	}
	if pm.GetPlugin("my-plugin").Enabled {
		t.Error("should be disabled")
	}

	if err := pm.EnablePlugin("my-plugin"); err != nil {
		t.Fatal(err)
	}
	if !pm.GetPlugin("my-plugin").Enabled {
		t.Error("should be enabled")
	}

	if err := pm.EnablePlugin("nonexistent"); err == nil {
		t.Error("expected error for nonexistent plugin")
	}
}

func TestManager_EnabledPlugins(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")

	setupPlugin(t, pluginsDir, "a", Plugin{Name: "a"}, nil)
	setupPlugin(t, pluginsDir, "b", Plugin{Name: "b"}, nil)

	pm := &Manager{plugins: make(map[string]*Plugin)}
	pm.loadFromDir(pluginsDir)
	pm.DisablePlugin("b")

	enabled := pm.EnabledPlugins()
	if len(enabled) != 1 {
		t.Errorf("expected 1 enabled, got %d", len(enabled))
	}
}

func TestManager_FormatList(t *testing.T) {
	pm := &Manager{plugins: make(map[string]*Plugin)}
	list := pm.FormatList()
	if list == "" {
		t.Error("expected non-empty format for empty list")
	}

	pm.plugins["test"] = &Plugin{Name: "test", Version: "1.0", Description: "desc", Enabled: true}
	list = pm.FormatList()
	if !contains(list, "test") || !contains(list, "enabled") {
		t.Errorf("format missing info: %s", list)
	}
}

func TestNewManager_MissingDirs(t *testing.T) {
	pm := NewManager("/nonexistent", "/nonexistent")
	if len(pm.GetPlugins()) != 0 {
		t.Error("expected 0 plugins from nonexistent dirs")
	}
}

func contains(s, sub string) bool {
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
