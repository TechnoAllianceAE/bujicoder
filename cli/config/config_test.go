package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes content to <dir>/bujicoder.yaml.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, unifiedFileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadUnifiedConfigMissingFile(t *testing.T) {
	t.Setenv("BUJICODER_CONFIG_DIR", t.TempDir())

	cfg, err := LoadUnifiedConfig()
	if err != nil {
		t.Fatalf("missing config must not be an error, got %v", err)
	}
	if cfg != nil {
		t.Fatalf("missing config must return nil to trigger first-run setup, got %+v", cfg)
	}
}

func TestLoadUnifiedConfigMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)
	path := writeConfig(t, dir, "mode: local\napi_keys: [this is not a mapping\n")

	cfg, err := LoadUnifiedConfig()
	if err == nil {
		t.Fatal("malformed YAML must return an error, not nil (nil would trigger the setup wizard and overwrite the user's keys)")
	}
	if cfg != nil {
		t.Fatalf("malformed YAML must not return a config, got %+v", cfg)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error must name the offending file %q, got %q", path, err)
	}
}

func TestLoadUnifiedConfigDefaults(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantMode string
	}{
		{
			name:     "explicit mode preserved",
			content:  "mode: local\ncost_mode: heavy\n",
			wantMode: "local",
		},
		{
			// A hand-edited config that dropped "mode:" is still a real config.
			name:     "missing mode defaults to local",
			content:  "cost_mode: heavy\napi_keys:\n  openrouter: sk-test\n",
			wantMode: "local",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("BUJICODER_CONFIG_DIR", dir)
			writeConfig(t, dir, tc.content)

			cfg, err := LoadUnifiedConfig()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg == nil {
				t.Fatal("existing config must not be reported as absent")
			}
			if cfg.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", cfg.Mode, tc.wantMode)
			}
			if !cfg.IsLocalMode() {
				t.Error("IsLocalMode() = false, want true")
			}
			// Defaults must be filled in so model resolution has something to use.
			for _, mode := range []string{"normal", "heavy", "max"} {
				m, ok := cfg.Modes[mode]
				if !ok {
					t.Fatalf("default mapping for %q missing", mode)
				}
				if m.Main == "" {
					t.Errorf("default mapping for %q has empty main model", mode)
				}
			}
		})
	}
}

func TestLoadUnifiedConfigLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"mode":"local","cost_mode":"max"}`), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	cfg, err := LoadUnifiedConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg == nil {
		t.Fatal("legacy config must be migrated, got nil")
	}
	if cfg.CostMode != "max" {
		t.Errorf("CostMode = %q, want max", cfg.CostMode)
	}

	// A corrupt legacy file must be reported, not treated as a first run.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"mode":`), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if _, err := LoadUnifiedConfig(); err == nil {
		t.Error("corrupt legacy config must return an error")
	}
}

func TestSaveUnifiedConfigRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)

	in := DefaultUnifiedConfigForProvider("anthropic", "sk-secret-key")
	in.RequestTimeout = 120
	in.MCPServers = []MCPServerConfig{{Name: "browser", Command: "npx", Args: []string{"-y", "srv"}, Lazy: true}}

	path, err := SaveUnifiedConfig(in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if want := filepath.Join(dir, unifiedFileName); path != want {
		t.Errorf("saved path = %q, want %q", path, want)
	}

	// The file holds API keys: it must not be world- or group-readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config permissions = %#o, want 0600 (file contains API keys)", perm)
	}

	// No temp file may be left behind by the atomic write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != unifiedFileName {
			t.Errorf("unexpected leftover file %q after save", e.Name())
		}
	}

	out, err := LoadUnifiedConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out == nil {
		t.Fatal("saved config did not load back")
	}
	if out.APIKeys.Anthropic != "sk-secret-key" {
		t.Errorf("Anthropic key = %q, want sk-secret-key", out.APIKeys.Anthropic)
	}
	if out.RequestTimeout != 120 {
		t.Errorf("RequestTimeout = %d, want 120", out.RequestTimeout)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].Name != "browser" || len(out.MCPServers[0].Args) != 2 {
		t.Errorf("MCPServers round-trip mismatch: %+v", out.MCPServers)
	}
}

func TestSaveUnifiedConfigOverwriteKeepsFileComplete(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)

	first := DefaultUnifiedConfig("sk-one")
	if _, err := SaveUnifiedConfig(first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second := DefaultUnifiedConfig("sk-two-much-longer-key-value")
	if _, err := SaveUnifiedConfig(second); err != nil {
		t.Fatalf("second save: %v", err)
	}

	cfg, err := LoadUnifiedConfig()
	if err != nil {
		t.Fatalf("load after overwrite: %v", err)
	}
	if cfg.APIKeys.OpenRouter != "sk-two-much-longer-key-value" {
		t.Errorf("OpenRouter key = %q, want the second key", cfg.APIKeys.OpenRouter)
	}
	info, err := os.Stat(filepath.Join(dir, unifiedFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions after overwrite = %#o, want 0600", perm)
	}
}

func TestGetAPIKey(t *testing.T) {
	cfg := &UnifiedConfig{APIKeys: APIKeysConfig{
		OpenRouter: "cfg-openrouter",
		Anthropic:  "cfg-anthropic",
		GoogleAI:   "cfg-google",
		OllamaURL:  "http://cfg-ollama",
	}}

	tests := []struct {
		name     string
		provider string
		env      map[string]string
		want     string
	}{
		{name: "config value wins", provider: "openrouter", want: "cfg-openrouter"},
		{name: "alias resolves to same field", provider: "gemini", want: "cfg-google"},
		{name: "case insensitive", provider: "ANTHROPIC", want: "cfg-anthropic"},
		{name: "url provider", provider: "ollama", want: "http://cfg-ollama"},
		{
			name:     "env fallback when unset in config",
			provider: "groq",
			env:      map[string]string{"GROQ_API_KEY": "env-groq"},
			want:     "env-groq",
		},
		{
			name:     "config takes precedence over env",
			provider: "anthropic",
			env:      map[string]string{"ANTHROPIC_API_KEY": "env-anthropic"},
			want:     "cfg-anthropic",
		},
		{
			name:     "unknown provider has no env mapping",
			provider: "totally-unknown",
			env:      map[string]string{"TOTALLY_UNKNOWN_API_KEY": "nope"},
			want:     "",
		},
		{name: "empty provider", provider: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := cfg.GetAPIKey(tc.provider); got != tc.want {
				t.Errorf("GetAPIKey(%q) = %q, want %q", tc.provider, got, tc.want)
			}
		})
	}
}

func TestGetAgentsDirPrecedence(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", cfgDir)

	cfg := &UnifiedConfig{AgentsDir: "/from/config"}

	t.Setenv("BUJICODER_AGENTS_DIR", "/from/env")
	if got := cfg.GetAgentsDir(); got != "/from/env" {
		t.Errorf("env override: got %q, want /from/env", got)
	}

	t.Setenv("BUJICODER_AGENTS_DIR", "")
	if got := cfg.GetAgentsDir(); got != "/from/config" {
		t.Errorf("config value: got %q, want /from/config", got)
	}

	empty := &UnifiedConfig{}
	got := empty.GetAgentsDir()
	if got == "" {
		t.Fatal("GetAgentsDir() must never be empty")
	}
	// With neither env nor config set, the fallback is under the config dir
	// unless an agents dir sits next to the test binary.
	if !filepath.IsAbs(got) {
		t.Errorf("fallback agents dir must be absolute, got %q", got)
	}
}

func TestDirHonoursEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)
	if got := Dir(); got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
}

func TestToModelConfigAndLegacy(t *testing.T) {
	cfg := &UnifiedConfig{
		Mode:     "local",
		CostMode: "heavy",
		Modes: map[string]UnifiedModeMapping{
			"heavy": {Main: "m", FileExplorer: "f", SubAgent: "s", AgentOverrides: map[string]string{"editor": "e"}},
		},
	}

	mc := cfg.ToModelConfig()
	mapping, ok := mc.Modes["heavy"]
	if !ok {
		t.Fatal("heavy mode missing from converted model config")
	}
	if mapping.Main != "m" || mapping.FileExplorer != "f" || mapping.SubAgent != "s" {
		t.Errorf("mapping not carried over: %+v", mapping)
	}
	if mapping.AgentOverrides["editor"] != "e" {
		t.Errorf("agent overrides not carried over: %+v", mapping.AgentOverrides)
	}

	legacy := cfg.ToLegacyConfig()
	if legacy.Mode != "local" || legacy.CostMode != "heavy" {
		t.Errorf("legacy conversion mismatch: %+v", legacy)
	}
}
