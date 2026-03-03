// Package agent provides YAML agent definition loading, registry, step runner, and tool dispatch.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
	"gopkg.in/yaml.v3"
)

// Definition represents a YAML agent definition.
type Definition struct {
	ID               string   `yaml:"id" json:"id"`
	Version          string   `yaml:"version" json:"version"`
	DisplayName      string   `yaml:"display_name" json:"display_name"`
	Publisher        string   `yaml:"publisher" json:"publisher"`
	Model            string   `yaml:"model" json:"model"`
	OutputMode       string   `yaml:"output_mode" json:"output_mode"` // "last_message", "full_conversation"
	Tools            []string `yaml:"tools" json:"tools"`
	SpawnableAgents  []string `yaml:"spawnable_agents" json:"spawnable_agents"`
	SystemPrompt     string   `yaml:"system_prompt" json:"system_prompt"`
	InstructionsPrompt string `yaml:"instructions_prompt" json:"instructions_prompt"`
	MaxSteps         int      `yaml:"max_steps" json:"max_steps"`
	MaxTokens        int      `yaml:"max_tokens" json:"max_tokens"`
}

// Registry holds loaded agent definitions.
type Registry struct {
	agents map[string]*Definition
}

// NewRegistry creates an empty agent registry.
func NewRegistry() *Registry {
	return &Registry{agents: make(map[string]*Definition)}
}

// LoadDir loads all YAML agent definitions from a directory.
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read agent dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		def, err := LoadFile(path)
		if err != nil {
			return fmt.Errorf("load agent %s: %w", path, err)
		}

		r.agents[def.ID] = def
	}

	return nil
}

// Get retrieves an agent definition by ID.
func (r *Registry) Get(id string) (*Definition, bool) {
	def, ok := r.agents[id]
	return def, ok
}

// List returns all registered agent definitions in stable alphabetical order.
func (r *Registry) List() []*Definition {
	defs := make([]*Definition, 0, len(r.agents))
	for _, def := range r.agents {
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].ID < defs[j].ID
	})
	return defs
}

// Register adds or replaces an agent definition.
func (r *Registry) Register(def *Definition) {
	r.agents[def.ID] = def
}

// WithCostMode returns a copy of the definition with its model replaced
// according to the given cost mode using the server-side resolver.
// The agent's ID is used to determine the agent role for model resolution.
// If the resolver is nil, the definition is returned unchanged.
func (d *Definition) WithCostMode(mode costmode.Mode, resolver *costmode.Resolver) *Definition {
	if resolver == nil {
		return d
	}
	cp := *d // shallow copy
	role := costmode.RoleSubAgent
	switch cp.ID {
	case "base":
		role = costmode.RoleMain
	case "file_explorer":
		role = costmode.RoleFileExplorer
	}
	if model := resolver.ResolveModelForAgent(mode, role, cp.ID); model != "" {
		cp.Model = model
	}
	return &cp
}

// LoadFile loads a single YAML agent definition from a file.
func LoadFile(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var def Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if def.ID == "" {
		return nil, fmt.Errorf("agent definition missing 'id' field")
	}
	// Defaults
	if def.OutputMode == "" {
		def.OutputMode = "last_message"
	}
	if def.MaxSteps == 0 {
		def.MaxSteps = 50
	}
	if def.MaxTokens == 0 {
		def.MaxTokens = 8192
	}

	return &def, nil
}

// LoadBytes loads a single YAML agent definition from raw bytes.
func LoadBytes(data []byte, source string) (*Definition, error) {
	var def Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("parse yaml %s: %w", source, err)
	}
	if def.ID == "" {
		return nil, fmt.Errorf("agent definition %s missing 'id' field", source)
	}
	if def.OutputMode == "" {
		def.OutputMode = "last_message"
	}
	if def.MaxSteps == 0 {
		def.MaxSteps = 50
	}
	if def.MaxTokens == 0 {
		def.MaxTokens = 8192
	}
	return &def, nil
}
