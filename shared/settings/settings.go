// Package settings implements a 4-layer settings hierarchy for buji configuration.
// Priority (highest to lowest): managed > user > local > project.
// The existing YAML config (bujicoder.yaml) serves as the defaults layer.
package settings

import (
	"encoding/json"
	"fmt"
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
	// malformed is true when the file exists but could not be parsed. Writing
	// such a layer would replace the user's file with a single key and destroy
	// whatever was in it, so writes are refused instead.
	malformed bool
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
	l := layer{name: name, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		// A missing file is normal; anything else (unreadable, permission
		// denied) must not be treated as "empty and safe to overwrite".
		l.malformed = !os.IsNotExist(err)
		h.layers = append(h.layers, l)
		return
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		l.malformed = true
		h.layers = append(h.layers, l)
		return
	}

	l.settings = settings
	h.layers = append(h.layers, l)
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
	return h.setInLayer("user", key, value)
}

// SetProject writes a setting to the project layer and persists to disk.
func (h *Hierarchy) SetProject(key string, value any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.setInLayer("project", key, value)
}

// setInLayer stores a key in the named layer and persists it. Callers must hold
// h.mu. A missing layer is an error rather than a silent no-op, which would
// look like a successful save to the caller.
func (h *Hierarchy) setInLayer(name, key string, value any) error {
	for i := range h.layers {
		if h.layers[i].name != name {
			continue
		}
		if h.layers[i].malformed {
			return fmt.Errorf("refusing to overwrite %s settings at %s: the existing file could not be parsed", name, h.layers[i].path)
		}
		if h.layers[i].settings == nil {
			h.layers[i].settings = make(map[string]any)
		}
		h.layers[i].settings[key] = value
		return h.saveLayer(h.layers[i])
	}
	return fmt.Errorf("no %s settings layer is configured", name)
}

// saveLayer writes a layer to disk atomically: writing in place would leave a
// truncated, unparseable settings file if the process died or the disk filled
// mid-write, losing every user setting.
func (h *Hierarchy) saveLayer(l layer) error {
	if l.path == "" {
		return nil
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l.settings, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Reload re-reads all layers from disk.
func (h *Hierarchy) Reload() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.layers {
		if h.layers[i].path == "" {
			continue
		}
		h.layers[i].settings = nil
		h.layers[i].malformed = false
		data, err := os.ReadFile(h.layers[i].path)
		if err != nil {
			h.layers[i].malformed = !os.IsNotExist(err)
			continue
		}
		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			h.layers[i].malformed = true
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
