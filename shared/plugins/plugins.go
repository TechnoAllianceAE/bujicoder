// Package plugins implements a plugin system for extending buji with
// custom commands, hooks, and MCP server configurations.
// Plugins are directories with a plugin.json manifest, installed under
// ~/.bujicoder/plugins/ (user) or .bujicoder/plugins/ (project).
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Plugin represents a loaded plugin.
type Plugin struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Author      string            `json:"author"`
	Enabled     bool              `json:"enabled"`
	Path        string            `json:"-"`
	Commands    []Command         `json:"commands,omitempty"`
	Hooks       []Hook            `json:"hooks,omitempty"`
	MCPServers  map[string]any    `json:"mcpServers,omitempty"`
}

// Command is a slash command provided by a plugin.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

// Hook is a lifecycle hook registered by a plugin.
type Hook struct {
	Event   string `json:"event"`   // "PreToolUse", "PostToolUse"
	Command string `json:"command"` // shell command to run
}

// Manager discovers and loads plugins.
type Manager struct {
	plugins map[string]*Plugin
}

// NewManager creates a plugin manager that loads from the given directories.
func NewManager(configDir, projectRoot string) *Manager {
	pm := &Manager{
		plugins: make(map[string]*Plugin),
	}

	// User plugins: ~/.bujicoder/plugins/
	pm.loadFromDir(filepath.Join(configDir, "plugins"))

	// Project plugins: .bujicoder/plugins/
	if projectRoot != "" {
		pm.loadFromDir(filepath.Join(projectRoot, ".bujicoder", "plugins"))
	}

	return pm
}

// GetPlugins returns all loaded plugins.
func (pm *Manager) GetPlugins() map[string]*Plugin {
	return pm.plugins
}

// GetPlugin returns a plugin by name, or nil.
func (pm *Manager) GetPlugin(name string) *Plugin {
	return pm.plugins[name]
}

// EnablePlugin enables a plugin by name.
func (pm *Manager) EnablePlugin(name string) error {
	p, ok := pm.plugins[name]
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}
	p.Enabled = true
	return nil
}

// DisablePlugin disables a plugin by name.
func (pm *Manager) DisablePlugin(name string) error {
	p, ok := pm.plugins[name]
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}
	p.Enabled = false
	return nil
}

// EnabledPlugins returns only enabled plugins.
func (pm *Manager) EnabledPlugins() []*Plugin {
	var result []*Plugin
	for _, p := range pm.plugins {
		if p.Enabled {
			result = append(result, p)
		}
	}
	return result
}

// FormatList returns a formatted display of all plugins.
func (pm *Manager) FormatList() string {
	if len(pm.plugins) == 0 {
		return "No plugins installed.\n\nInstall plugins by placing them in ~/.bujicoder/plugins/"
	}
	var sb strings.Builder
	sb.WriteString("Installed Plugins:\n")
	for _, p := range pm.plugins {
		status := "disabled"
		if p.Enabled {
			status = "enabled"
		}
		fmt.Fprintf(&sb, "  [%s] %s v%s — %s\n", status, p.Name, p.Version, p.Description)
	}
	return sb.String()
}

func (pm *Manager) loadFromDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginDir := filepath.Join(dir, entry.Name())
		manifestPath := filepath.Join(pluginDir, "plugin.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		var plugin Plugin
		if err := json.Unmarshal(data, &plugin); err != nil {
			continue
		}
		plugin.Path = pluginDir
		if plugin.Name == "" {
			plugin.Name = entry.Name()
		}
		plugin.Enabled = true

		// Load commands from commands/ directory
		cmdDir := filepath.Join(pluginDir, "commands")
		if cmdEntries, err := os.ReadDir(cmdDir); err == nil {
			for _, ce := range cmdEntries {
				if strings.HasSuffix(ce.Name(), ".md") {
					content, _ := os.ReadFile(filepath.Join(cmdDir, ce.Name()))
					plugin.Commands = append(plugin.Commands, Command{
						Name:    strings.TrimSuffix(ce.Name(), ".md"),
						Content: string(content),
					})
				}
			}
		}

		pm.plugins[plugin.Name] = &plugin
	}
}
