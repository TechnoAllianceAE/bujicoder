// Package features implements build-time and runtime feature flags.
// Flags can be toggled via environment variables (BUJI_FEATURE_<NAME>=true)
// or programmatically at runtime.
package features

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Flag represents a feature flag.
type Flag struct {
	Name        string
	Description string
	Category    string // "agent", "ui", "tool", "rollout"
	Enabled     bool
}

// Registry holds all feature flags.
type Registry struct {
	mu    sync.RWMutex
	flags map[string]*Flag
}

// DefaultRegistry returns the global feature flag registry with all known flags.
func DefaultRegistry() *Registry {
	r := &Registry{flags: make(map[string]*Flag)}

	// Agent & Memory
	r.add("AGENT_MEMORY_SNAPSHOT", "Custom agent memory snapshots", "agent")
	r.add("AGENT_TRIGGERS", "Local cron/trigger tools", "agent")
	r.add("AGENT_TRIGGERS_REMOTE", "Remote trigger tool", "agent")
	r.add("BUILTIN_EXPLORE_PLAN_AGENTS", "Built-in agent presets", "agent")
	r.add("EXTRACT_MEMORIES", "Post-query memory extraction", "agent")
	r.add("VERIFICATION_AGENT", "Verification guidance", "agent")
	r.add("TEAMMEM", "Team memory files & watchers", "agent")

	// UI & Interaction
	r.add("AWAY_SUMMARY", "Away-from-keyboard summary", "ui")
	r.add("HISTORY_PICKER", "Interactive history picker", "ui")
	r.add("BRIEF_MODE", "Brief-only transcript mode", "ui")
	r.add("MESSAGE_ACTIONS", "Message action entrypoints", "ui")
	r.add("QUICK_SEARCH", "Prompt quick-search", "ui")
	r.add("TOKEN_BUDGET", "Token budget tracking", "ui")
	r.add("ULTRAPLAN", "Extended planning", "ui")
	r.add("ULTRATHINK", "Extra thinking depth", "ui")
	r.add("VOICE_MODE", "Voice toggling & dictation", "ui")
	r.add("GUI_MODE", "Desktop GUI via Wails", "ui")

	// Tools & Permissions
	r.add("BASH_CLASSIFIER", "Classifier-assisted bash decisions", "tool")
	r.add("BRIDGE_MODE", "Remote Control bridge", "tool")
	r.add("POWERSHELL_AUTO_MODE", "PowerShell auto-mode", "tool")

	// Compaction & Context
	r.add("CACHED_MICROCOMPACT", "Cached compaction", "tool")
	r.add("COMPACTION_REMINDERS", "Compaction UI copy", "tool")
	r.add("PROMPT_CACHE_DETECTION", "Cache break detection", "tool")

	return r
}

func (r *Registry) add(name, description, category string) {
	r.flags[name] = &Flag{
		Name:        name,
		Description: description,
		Category:    category,
		Enabled:     false,
	}
}

// IsEnabled checks if a feature flag is enabled.
// Environment variable BUJI_FEATURE_<NAME>=true overrides the registry.
func (r *Registry) IsEnabled(name string) bool {
	envKey := "BUJI_FEATURE_" + strings.ToUpper(name)
	if val := os.Getenv(envKey); val != "" {
		return val == "true" || val == "1"
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	flag, ok := r.flags[name]
	if !ok {
		return false
	}
	return flag.Enabled
}

// Enable turns on a feature flag.
func (r *Registry) Enable(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.flags[name]; ok {
		f.Enabled = true
	}
}

// Disable turns off a feature flag.
func (r *Registry) Disable(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.flags[name]; ok {
		f.Enabled = false
	}
}

// Toggle flips a feature flag.
func (r *Registry) Toggle(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.flags[name]; ok {
		f.Enabled = !f.Enabled
		return f.Enabled
	}
	return false
}

// List returns all flags sorted by category then name.
func (r *Registry) List() []*Flag {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Flag, 0, len(r.flags))
	for _, f := range r.flags {
		result = append(result, f)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return result[i].Category < result[j].Category
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// FormatList returns a human-readable display of all feature flags.
func (r *Registry) FormatList() string {
	flags := r.List()
	if len(flags) == 0 {
		return "No feature flags defined."
	}

	var sb strings.Builder
	sb.WriteString("Feature Flags:\n")
	currentCategory := ""
	for _, f := range flags {
		if f.Category != currentCategory {
			currentCategory = f.Category
			fmt.Fprintf(&sb, "\n  [%s]\n", strings.ToUpper(currentCategory))
		}
		status := "off"
		if r.IsEnabled(f.Name) {
			status = "ON"
		}
		fmt.Fprintf(&sb, "    %-35s %3s   %s\n", f.Name, status, f.Description)
	}
	fmt.Fprintf(&sb, "\nToggle: set env BUJI_FEATURE_<NAME>=true\n")
	return sb.String()
}
