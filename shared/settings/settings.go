// Package settings implements a 4-layer settings hierarchy for buji configuration.
// Priority (highest to lowest): managed > user > local > project.
// The existing YAML config (bujicoder.yaml) serves as the defaults layer.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Hierarchy manages layered settings from multiple sources.
type Hierarchy struct {
	mu     sync.RWMutex
	layers []layer // ordered from highest to lowest priority
}

type layer struct {
	name     string
	path     string
	settings map[string]any
}

// NewHierarchy creates a settings hierarchy from the standard config locations.
// configDir is typically ~/.bujicoder/, projectRoot is the working directory.
func NewHierarchy(configDir, projectRoot string) *Hierarchy {
	h := &Hierarchy{}

	// Load layers in priority order (highest first)
	h.loadLayer("managed", filepath.Join(configDir, "managed-settings.json"))
	h.loadLayer("user", filepath.Join(configDir, "settings.json"))
	if projectRoot != "" {
		h.loadLayer("local", filepath.Join(projectRoot, ".bujicoder", "settings.local.json"))
		h.loadLayer("project", filepath.Join(projectRoot, ".bujicoder", "settings.json"))
	}

	return h
}

func (h *Hierarchy) loadLayer(name, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		h.layers = append(h.layers, layer{name: name, path: path, settings: nil})
		return
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		h.layers = append(h.layers, layer{name: name, path: path, settings: nil})
		return
	}

	h.layers = append(h.layers, layer{name: name, path: path, settings: settings})
}

// Get returns the value of a setting, searching layers from highest to lowest
// priority. Returns the defaultValue if not found in any layer.
func (h *Hierarchy) Get(key string, defaultValue any) any {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, l := range h.layers {
		if l.settings == nil {
			continue
		}
		if val, ok := l.settings[key]; ok {
			return val
		}
	}
	return defaultValue
}

// GetString is a convenience method that returns a string setting.
func (h *Hierarchy) GetString(key, defaultValue string) string {
	val := h.Get(key, defaultValue)
	if s, ok := val.(string); ok {
		return s
	}
	return defaultValue
}

// GetBool is a convenience method that returns a boolean setting.
func (h *Hierarchy) GetBool(key string, defaultValue bool) bool {
	val := h.Get(key, defaultValue)
	if b, ok := val.(bool); ok {
		return b
	}
	return defaultValue
}

// GetStringSlice returns a string slice setting.
func (h *Hierarchy) GetStringSlice(key string) []string {
	val := h.Get(key, nil)
	if val == nil {
		return nil
	}
	arr, ok := val.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// Set writes a setting to the user layer and persists to disk.
func (h *Hierarchy) Set(key string, value any) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Find the user layer
	for i := range h.layers {
		if h.layers[i].name == "user" {
			if h.layers[i].settings == nil {
				h.layers[i].settings = make(map[string]any)
			}
			h.layers[i].settings[key] = value
			return h.saveLayer(h.layers[i])
		}
	}
	return nil
}

// SetProject writes a setting to the project layer and persists to disk.
func (h *Hierarchy) SetProject(key string, value any) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.layers {
		if h.layers[i].name == "project" {
			if h.layers[i].settings == nil {
				h.layers[i].settings = make(map[string]any)
			}
			h.layers[i].settings[key] = value
			return h.saveLayer(h.layers[i])
		}
	}
	return nil
}

func (h *Hierarchy) saveLayer(l layer) error {
	if l.path == "" {
		return nil
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, data, 0644)
}

// Reload re-reads all layers from disk.
func (h *Hierarchy) Reload() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.layers {
		if h.layers[i].path == "" {
			continue
		}
		data, err := os.ReadFile(h.layers[i].path)
		if err != nil {
			h.layers[i].settings = nil
			continue
		}
		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			h.layers[i].settings = nil
			continue
		}
		h.layers[i].settings = settings
	}
}

// GetAllFromLayer returns all settings from a specific layer (for display).
func (h *Hierarchy) GetAllFromLayer(layerName string) map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, l := range h.layers {
		if l.name == layerName && l.settings != nil {
			cp := make(map[string]any, len(l.settings))
			for k, v := range l.settings {
				cp[k] = v
			}
			return cp
		}
	}
	return nil
}

// LayerNames returns the names of all layers in priority order.
func (h *Hierarchy) LayerNames() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	names := make([]string, len(h.layers))
	for i, l := range h.layers {
		names[i] = l.name
	}
	return names
}
