// Package costmode defines the cost mode system that maps agent roles to
// specific LLM models based on cost/quality trade-offs.
//
// Model mappings are loaded from a YAML config file on the server, not
// hardcoded. The CLI sends a mode string; the server resolves the model.
package costmode

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// Mode represents a cost/quality trade-off for LLM model selection.
type Mode string

const (
	ModeNormal Mode = "normal"
	ModeHeavy  Mode = "heavy"
	ModeMax    Mode = "max"
)

// AgentRole identifies the type of agent for model resolution.
type AgentRole string

const (
	RoleMain         AgentRole = "main"
	RoleFileExplorer AgentRole = "file_explorer"
	RoleSubAgent     AgentRole = "sub_agent"

	// DefaultFallbackModel is used when the config has no model for a role.
	DefaultFallbackModel = "x-ai/grok-code-fast-1"
)

// ModelMapping maps agent roles to specific models for a given cost mode.
type ModelMapping struct {
	Main           string            `yaml:"main" json:"main"`
	FileExplorer   string            `yaml:"file_explorer" json:"file_explorer"`
	SubAgent       string            `yaml:"sub_agent" json:"sub_agent"`
	AgentOverrides map[string]string `yaml:"agent_overrides,omitempty" json:"agent_overrides,omitempty"`
}

// ModelConfig is the YAML structure for the model configuration file.
type ModelConfig struct {
	Modes map[Mode]ModelMapping `yaml:"modes" json:"modes"`
}

// Resolver resolves LLM models for a given cost mode and agent role.
// It loads its mappings from a YAML config file on the server and supports
// live updates via the admin API.
type Resolver struct {
	mu       sync.RWMutex
	config   ModelConfig
	filePath string
}

// NewResolver creates a Resolver from a YAML config file.
// If the file does not exist or cannot be read, it returns an error.
func NewResolver(filePath string) (*Resolver, error) {
	cfg, err := LoadModelConfig(filePath)
	if err != nil {
		return nil, err
	}
	return &Resolver{
		config:   *cfg,
		filePath: filePath,
	}, nil
}

// NewResolverFromConfig creates a Resolver from an in-memory ModelConfig.
// Useful for tests where no file is needed.
func NewResolverFromConfig(cfg ModelConfig) *Resolver {
	return &Resolver{config: cfg}
}

// ResolveModel returns the model string for a given cost mode and agent role.
// Falls back to ModeNormal if the mode is unknown, and returns an empty string
// only if ModeNormal is also missing (which should not happen with a valid config).
func (r *Resolver) ResolveModel(mode Mode, role AgentRole) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.resolveModelLocked(mode, role)
}

// ResolveModelForAgent returns the model string for a given cost mode, agent role,
// and specific agent ID. It checks per-agent overrides first, then falls back to
// role-based resolution. This allows individual sub-agents to use different models.
func (r *Resolver) ResolveModelForAgent(mode Mode, role AgentRole, agentID string) string {
	model, _ := r.ResolveModelForAgentWithFallback(mode, role, agentID)
	return model
}

// ResolveModelForAgentWithFallback is like ResolveModelForAgent but also returns
// whether the default fallback model was used (i.e. no config entry was found).
func (r *Resolver) ResolveModelForAgentWithFallback(mode Mode, role AgentRole, agentID string) (model string, isFallback bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	mapping, ok := r.config.Modes[mode]
	if !ok {
		mapping, ok = r.config.Modes[ModeNormal]
		if !ok {
			return DefaultFallbackModel, true
		}
	}

	if agentID != "" && mapping.AgentOverrides != nil {
		if m, exists := mapping.AgentOverrides[agentID]; exists && m != "" {
			return m, false
		}
	}

	if m := r.resolveRoleLocked(mapping, role); m != "" {
		return m, false
	}
	return DefaultFallbackModel, true
}

func (r *Resolver) resolveModelLocked(mode Mode, role AgentRole) string {
	mapping, ok := r.config.Modes[mode]
	if !ok {
		mapping, ok = r.config.Modes[ModeNormal]
		if !ok {
			return DefaultFallbackModel
		}
	}
	if model := r.resolveRoleLocked(mapping, role); model != "" {
		return model
	}
	return DefaultFallbackModel
}

func (r *Resolver) resolveRoleLocked(mapping ModelMapping, role AgentRole) string {
	switch role {
	case RoleFileExplorer:
		return mapping.FileExplorer
	case RoleSubAgent:
		return mapping.SubAgent
	default:
		return mapping.Main
	}
}

// GetConfig returns a copy of the current model configuration.
func (r *Resolver) GetConfig() ModelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Deep-copy the modes map including agent overrides.
	copy := ModelConfig{Modes: make(map[Mode]ModelMapping, len(r.config.Modes))}
	for k, v := range r.config.Modes {
		m := v
		if v.AgentOverrides != nil {
			m.AgentOverrides = make(map[string]string, len(v.AgentOverrides))
			for ak, av := range v.AgentOverrides {
				m.AgentOverrides[ak] = av
			}
		}
		copy.Modes[k] = m
	}
	return copy
}

// UpdateConfig replaces the in-memory config and persists it to disk.
func (r *Resolver) UpdateConfig(cfg ModelConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Normalize empty agent override maps to nil so they are omitted from YAML.
	for k, v := range cfg.Modes {
		if len(v.AgentOverrides) == 0 {
			v.AgentOverrides = nil
			cfg.Modes[k] = v
		}
	}

	if r.filePath != "" {
		if err := saveModelConfig(r.filePath, &cfg); err != nil {
			return err
		}
	}
	r.config = cfg
	return nil
}

// LoadModelConfig reads a ModelConfig from a YAML file.
func LoadModelConfig(path string) (*ModelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model config %s: %w", path, err)
	}

	var cfg ModelConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse model config %s: %w", path, err)
	}

	if cfg.Modes == nil {
		cfg.Modes = make(map[Mode]ModelMapping)
	}
	return &cfg, nil
}

// saveModelConfig writes a ModelConfig to a YAML file.
func saveModelConfig(path string, cfg *ModelConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal model config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write model config %s: %w", path, err)
	}
	return nil
}

// Valid returns true if the mode is a recognized cost mode.
func (m Mode) Valid() bool {
	switch m {
	case ModeNormal, ModeHeavy, ModeMax:
		return true
	default:
		return false
	}
}

// String returns the string representation of the mode.
func (m Mode) String() string {
	return string(m)
}

// ParseMode parses a string into a Mode, returning ModeNormal for unknown values.
func ParseMode(s string) Mode {
	m := Mode(s)
	if m.Valid() {
		return m
	}
	return ModeNormal
}

// AllModes returns all valid cost modes.
func AllModes() []Mode {
	return []Mode{ModeNormal, ModeHeavy, ModeMax}
}
