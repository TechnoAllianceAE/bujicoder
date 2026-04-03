// Package config manages the ~/.bujicoder/ configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
	"gopkg.in/yaml.v3"
)

const (
	configDirName   = ".bujicoder"
	unifiedFileName = "bujicoder.yaml"
)

// Config holds CLI configuration.
type Config struct {
	CostMode        string `json:"cost_mode,omitempty" yaml:"cost_mode,omitempty"`
	Mode            string `json:"mode,omitempty" yaml:"mode,omitempty"`                           // always "local"
	AgentsDir       string `json:"agents_dir,omitempty" yaml:"agents_dir,omitempty"`               // path to agents/ yaml dir
	ModelConfigPath string `json:"model_config_path,omitempty" yaml:"model_config_path,omitempty"` // path to model_config.yaml
}

// GetAgentsDir returns the agents directory, checking env var, config, then default.
func (c *Config) GetAgentsDir() string {
	if d := os.Getenv("BUJICODER_AGENTS_DIR"); d != "" {
		return d
	}
	if c.AgentsDir != "" {
		return c.AgentsDir
	}
	return "./agents"
}

// GetModelConfigPath returns the model config path, checking env var, config, then default.
func (c *Config) GetModelConfigPath() string {
	if p := os.Getenv("BUJICODER_MODEL_CONFIG"); p != "" {
		return p
	}
	if c.ModelConfigPath != "" {
		return c.ModelConfigPath
	}
	return "./model_config.yaml"
}

// Dir returns the config directory path.
func Dir() string {
	if d := os.Getenv("BUJICODER_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, configDirName)
}

// ExeDir returns the directory containing the running executable.
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return filepath.Dir(exe)
	}
	return filepath.Dir(resolved)
}

// ---------------------------------------------------------------------------
// Unified YAML Config (bujicoder.yaml)
// ---------------------------------------------------------------------------

// UnifiedConfig is the single-file YAML config for BujiCoder.
type UnifiedConfig struct {
	Mode           string                        `yaml:"mode"`      // "local"
	CostMode       string                        `yaml:"cost_mode"` // "normal", "heavy", "max"
	APIKeys        APIKeysConfig                 `yaml:"api_keys"`
	Modes          map[string]UnifiedModeMapping `yaml:"modes"` // inline model config
	AgentsDir      string                        `yaml:"agents_dir,omitempty"`
	MCPServers     []MCPServerConfig             `yaml:"mcp_servers,omitempty"` // MCP tool servers
	RequestTimeout int                           `yaml:"request_timeout,omitempty"` // LLM request timeout in seconds (default: 90)
}

// MCPServerConfig describes how to launch an MCP server process.
type MCPServerConfig struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Lazy    bool     `yaml:"lazy"` // If true, only start when a tool from this server is first invoked
}

// APIKeysConfig holds API keys for various LLM providers.
type APIKeysConfig struct {
	OpenRouter string `yaml:"openrouter,omitempty"`
	Kilocode   string `yaml:"kilocode,omitempty"`
	Anthropic  string `yaml:"anthropic,omitempty"`
	OpenAI     string `yaml:"openai,omitempty"`
	GoogleAI   string `yaml:"google_ai,omitempty"`
	XAI        string `yaml:"xai,omitempty"`
	ZAI        string `yaml:"zai,omitempty"`
	Together   string `yaml:"together,omitempty"`
	Groq       string `yaml:"groq,omitempty"`
	Cerebras   string `yaml:"cerebras,omitempty"`
	OllamaURL  string `yaml:"ollama_url,omitempty"`
	LlamacppURL string `yaml:"llamacpp_url,omitempty"`
}

// UnifiedModeMapping maps agent roles to models for a given cost mode.
type UnifiedModeMapping struct {
	Main           string            `yaml:"main"`
	FileExplorer   string            `yaml:"file_explorer"`
	SubAgent       string            `yaml:"sub_agent"`
	AgentOverrides map[string]string `yaml:"agent_overrides,omitempty"`
}

// IsLocalMode returns true if the unified config is set to standalone local mode.
func (u *UnifiedConfig) IsLocalMode() bool {
	return u.Mode == "local"
}

// GetAgentsDir returns the agents directory from env var, config, or defaults.
func (u *UnifiedConfig) GetAgentsDir() string {
	if d := os.Getenv("BUJICODER_AGENTS_DIR"); d != "" {
		return d
	}
	if u.AgentsDir != "" {
		return u.AgentsDir
	}
	// Default: next to exe, then ~/.bujicoder/agents
	exeAgents := filepath.Join(ExeDir(), "agents")
	if info, err := os.Stat(exeAgents); err == nil && info.IsDir() {
		return exeAgents
	}
	return filepath.Join(Dir(), "agents")
}

// GetAPIKey returns the API key for a provider, checking config then env var fallback.
func (u *UnifiedConfig) GetAPIKey(provider string) string {
	provider = strings.ToLower(provider)
	var configVal string
	switch provider {
	case "kilocode", "kilo":
		configVal = u.APIKeys.Kilocode
	case "openrouter":
		configVal = u.APIKeys.OpenRouter
	case "anthropic":
		configVal = u.APIKeys.Anthropic
	case "openai":
		configVal = u.APIKeys.OpenAI
	case "google", "google_ai", "gemini":
		configVal = u.APIKeys.GoogleAI
	case "xai":
		configVal = u.APIKeys.XAI
	case "zai", "z-ai":
		configVal = u.APIKeys.ZAI
	case "together":
		configVal = u.APIKeys.Together
	case "groq":
		configVal = u.APIKeys.Groq
	case "cerebras":
		configVal = u.APIKeys.Cerebras
	case "ollama":
		configVal = u.APIKeys.OllamaURL
	case "llamacpp":
		configVal = u.APIKeys.LlamacppURL
	}
	if configVal != "" {
		return configVal
	}
	// Fall back to env var.
	envMap := map[string]string{
		"kilocode":   "KILOCODE_API_KEY",
		"kilo":       "KILOCODE_API_KEY",
		"openrouter": "OPENROUTER_API_KEY",
		"anthropic":  "ANTHROPIC_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"google":     "GOOGLE_AI_API_KEY",
		"google_ai":  "GOOGLE_AI_API_KEY",
		"gemini":     "GOOGLE_AI_API_KEY",
		"xai":        "XAI_API_KEY",
		"zai":        "ZAI_API_KEY",
		"z-ai":       "ZAI_API_KEY",
		"together":   "TOGETHER_API_KEY",
		"groq":       "GROQ_API_KEY",
		"cerebras":   "CEREBRAS_API_KEY",
		"ollama":     "OLLAMA_URL",
		"llamacpp":   "LLAMACPP_URL",
	}
	if envVar, ok := envMap[provider]; ok {
		return os.Getenv(envVar)
	}
	return ""
}

// ToModelConfig converts the inline Modes to a costmode.ModelConfig.
func (u *UnifiedConfig) ToModelConfig() costmode.ModelConfig {
	cfg := costmode.ModelConfig{
		Modes: make(map[costmode.Mode]costmode.ModelMapping),
	}
	for modeName, mapping := range u.Modes {
		cfg.Modes[costmode.Mode(modeName)] = costmode.ModelMapping{
			Main:           mapping.Main,
			FileExplorer:   mapping.FileExplorer,
			SubAgent:       mapping.SubAgent,
			AgentOverrides: mapping.AgentOverrides,
		}
	}
	return cfg
}

// ToLegacyConfig converts a UnifiedConfig to the legacy Config struct for backward compat.
func (u *UnifiedConfig) ToLegacyConfig() *Config {
	return &Config{
		Mode:     u.Mode,
		CostMode: u.CostMode,
	}
}

// UnifiedConfigPath returns the resolved path where the unified config was found,
// or empty string if not found.
func UnifiedConfigPath() string {
	// 1. ~/.bujicoder/bujicoder.yaml (canonical location)
	p := filepath.Join(Dir(), unifiedFileName)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	// 2. Next to executable (portable installs)
	p = filepath.Join(ExeDir(), unifiedFileName)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// LoadUnifiedConfig loads the unified YAML config from standard locations.
// Returns nil if no config is found at any location (triggers first-run setup).
func LoadUnifiedConfig() *UnifiedConfig {
	// 1. ~/.bujicoder/bujicoder.yaml (canonical location)
	if cfg := loadUnifiedFrom(filepath.Join(Dir(), unifiedFileName)); cfg != nil {
		return cfg
	}
	// 2. bujicoder.yaml next to executable (portable installs)
	if cfg := loadUnifiedFrom(filepath.Join(ExeDir(), unifiedFileName)); cfg != nil {
		return cfg
	}
	// 3. Legacy: ~/.bujicoder/config.json
	path := filepath.Join(Dir(), "config.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var legacy Config
		if json.Unmarshal(data, &legacy) == nil && legacy.Mode != "" {
			return legacyToUnified(&legacy)
		}
	}
	// 4. No config found -> first-run
	return nil
}

func loadUnifiedFrom(path string) *UnifiedConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg UnifiedConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	if cfg.Mode == "" {
		return nil
	}
	// Populate default mode->model mappings if missing.
	if len(cfg.Modes) == 0 {
		defaults := DefaultUnifiedConfig("")
		cfg.Modes = defaults.Modes
	}
	return &cfg
}

func legacyToUnified(legacy *Config) *UnifiedConfig {
	u := &UnifiedConfig{
		Mode:     legacy.Mode,
		CostMode: legacy.CostMode,
	}
	if u.Mode == "" {
		u.Mode = "local"
	}
	// Populate default mode->model mappings if missing.
	if len(u.Modes) == 0 {
		defaults := DefaultUnifiedConfig("")
		u.Modes = defaults.Modes
	}
	return u
}

// SaveUnifiedConfig writes the unified config as YAML to ~/.bujicoder/bujicoder.yaml.
func SaveUnifiedConfig(cfg *UnifiedConfig) (string, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}

	// Add header comment.
	content := "# BujiCoder Configuration\n# Edit this file to customize API keys, models, and settings.\n\n" + string(data)

	// Save to ~/.bujicoder/ (canonical location for config + agents).
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	homePath := filepath.Join(dir, unifiedFileName)
	if err := os.WriteFile(homePath, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return homePath, nil
}

// DefaultUnifiedConfig returns a default standalone config with the given API key.
func DefaultUnifiedConfig(openRouterKey string) *UnifiedConfig {
	return DefaultUnifiedConfigForProvider("openrouter", openRouterKey)
}

// DefaultUnifiedConfigForProvider returns a default config for the given provider.
// For openrouter, the defaults use multi-provider models routed through OpenRouter.
// For direct providers, the defaults use that provider's own models.
func DefaultUnifiedConfigForProvider(provider, apiKey string) *UnifiedConfig {
	cfg := &UnifiedConfig{
		Mode:     "local",
		CostMode: "normal",
	}

	switch provider {
	case "kilocode", "kilo":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {
				Main:         "anthropic/claude-sonnet-4-20250514",
				FileExplorer: "openai/gpt-4o-mini",
				SubAgent:     "google/gemini-2.5-flash",
				AgentOverrides: map[string]string{
					"editor":        "anthropic/claude-sonnet-4-20250514",
					"git_committer": "openai/gpt-4o-mini",
					"thinker":       "google/gemini-2.5-pro",
				},
			},
			"heavy": {
				Main:         "anthropic/claude-sonnet-4-20250514",
				FileExplorer: "openai/gpt-4o-mini",
				SubAgent:     "anthropic/claude-sonnet-4-20250514",
				AgentOverrides: map[string]string{
					"editor":   "anthropic/claude-sonnet-4-20250514",
					"thinker":  "google/gemini-2.5-pro",
					"reviewer": "openai/gpt-4o",
				},
			},
			"max": {
				Main:         "anthropic/claude-opus-4-20250514",
				FileExplorer: "openai/gpt-4o-mini",
				SubAgent:     "anthropic/claude-sonnet-4-20250514",
				AgentOverrides: map[string]string{
					"editor":   "anthropic/claude-sonnet-4-20250514",
					"thinker":  "anthropic/claude-opus-4-20250514",
					"reviewer": "anthropic/claude-opus-4-20250514",
					"planner":  "anthropic/claude-opus-4-20250514",
				},
			},
		}
	case "groq":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {Main: "groq/llama-3.3-70b-versatile", FileExplorer: "groq/llama-3.1-8b-instant", SubAgent: "groq/llama-3.3-70b-versatile"},
			"heavy":  {Main: "groq/llama-3.3-70b-versatile", FileExplorer: "groq/llama-3.1-8b-instant", SubAgent: "groq/llama-3.3-70b-versatile"},
			"max":    {Main: "groq/llama-3.3-70b-versatile", FileExplorer: "groq/llama-3.1-8b-instant", SubAgent: "groq/llama-3.3-70b-versatile"},
		}
	case "cerebras":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {Main: "cerebras/llama-3.3-70b", FileExplorer: "cerebras/llama-3.1-8b", SubAgent: "cerebras/llama-3.3-70b"},
			"heavy":  {Main: "cerebras/llama-3.3-70b", FileExplorer: "cerebras/llama-3.1-8b", SubAgent: "cerebras/llama-3.3-70b"},
			"max":    {Main: "cerebras/llama-3.3-70b", FileExplorer: "cerebras/llama-3.1-8b", SubAgent: "cerebras/llama-3.3-70b"},
		}
	case "together":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {Main: "together/meta-llama/Llama-3.3-70B-Instruct-Turbo", FileExplorer: "together/meta-llama/Llama-3.1-8B-Instruct-Turbo", SubAgent: "together/deepseek-ai/DeepSeek-V3.1"},
			"heavy":  {Main: "together/Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8", FileExplorer: "together/meta-llama/Llama-3.1-8B-Instruct-Turbo", SubAgent: "together/deepseek-ai/DeepSeek-V3.1"},
			"max":    {Main: "together/MiniMaxAI/MiniMax-M2.5", FileExplorer: "together/meta-llama/Llama-3.1-8B-Instruct-Turbo", SubAgent: "together/MiniMaxAI/MiniMax-M2.5"},
		}
	case "openai":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {Main: "openai/gpt-4o-mini", FileExplorer: "openai/gpt-4o-mini", SubAgent: "openai/gpt-4o-mini"},
			"heavy":  {Main: "openai/gpt-4o", FileExplorer: "openai/gpt-4o-mini", SubAgent: "openai/gpt-4o"},
			"max":    {Main: "openai/gpt-4o", FileExplorer: "openai/gpt-4o-mini", SubAgent: "openai/gpt-4o"},
		}
	case "anthropic":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {Main: "openai/gpt-oss-120b:free", FileExplorer: "openai/gpt-oss-120b", SubAgent: "openai/gpt-oss-120b:free"},
			"heavy":  {Main: "openai/gpt-oss-120b:free", FileExplorer: "openai/gpt-oss-120b", SubAgent: "openai/gpt-oss-120b:free"},
			"max":    {Main: "openai/gpt-oss-120b:free", FileExplorer: "openai/gpt-oss-120b", SubAgent: "openai/gpt-oss-120b:free"},
		}
	case "ollama":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {Main: "ollama/llama3:latest", FileExplorer: "ollama/llama3:latest", SubAgent: "ollama/llama3:latest"},
			"heavy":  {Main: "ollama/llama3:latest", FileExplorer: "ollama/llama3:latest", SubAgent: "ollama/llama3:latest"},
			"max":    {Main: "ollama/llama3:latest", FileExplorer: "ollama/llama3:latest", SubAgent: "ollama/llama3:latest"},
		}
	case "llamacpp":
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {Main: "llamacpp/local-model", FileExplorer: "llamacpp/local-model", SubAgent: "llamacpp/local-model"},
			"heavy":  {Main: "llamacpp/local-model", FileExplorer: "llamacpp/local-model", SubAgent: "llamacpp/local-model"},
			"max":    {Main: "llamacpp/local-model", FileExplorer: "llamacpp/local-model", SubAgent: "llamacpp/local-model"},
		}
	default: // openrouter — use current production defaults
		cfg.Modes = map[string]UnifiedModeMapping{
			"normal": {
				Main:         "qwen/qwen3.5-122b-a10b",
				FileExplorer: "openai/gpt-oss-20b",
				SubAgent:     "together/deepseek-ai/DeepSeek-V3.1",
				AgentOverrides: map[string]string{
					"editor":        "deepseek/deepseek-v3.2-speciale",
					"git_committer": "openai/gpt-oss-120b",
					"planner":       "z-ai/glm-5",
					"reviewer":      "together/moonshotai/Kimi-K2.5",
					"thinker":       "together/meta-llama/Llama-3.3-70B-Instruct-Turbo",
				},
			},
			"heavy": {
				Main:         "x-ai/grok-code-fast-1",
				FileExplorer: "openai/gpt-oss-20b",
				SubAgent:     "z-ai/glm-5",
				AgentOverrides: map[string]string{
					"editor":        "together/Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8",
					"git_committer": "openai/gpt-oss-120b",
					"reviewer":      "moonshotai/kimi-k2.5",
					"thinker":       "together/deepseek-ai/DeepSeek-R1",
				},
			},
			"max": {
				Main:         "minimax/minimax-m2.5",
				FileExplorer: "openai/gpt-oss-20b",
				SubAgent:     "together/MiniMaxAI/MiniMax-M2.5",
				AgentOverrides: map[string]string{
					"editor":          "together/Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8",
					"git_committer":   "openai/gpt-oss-120b",
					"implementor":     "together/MiniMaxAI/MiniMax-M2.5",
					"judge":           "minimax/minimax-m2.5",
					"parallel_editor": "minimax/minimax-m2.5",
					"planner":         "together/Qwen/Qwen3.5-397B-A17B",
					"researcher":      "together/MiniMaxAI/MiniMax-M2.5",
					"reviewer":        "qwen/qwen3.5-122b-a10b",
					"thinker":         "together/Qwen/Qwen3-235B-A22B-Thinking-2507",
				},
			},
		}
	}

	return cfg
}
