package costmode

import (
	"os"
	"path/filepath"
	"testing"
)

func testResolver() *Resolver {
	return NewResolverFromConfig(ModelConfig{
		Modes: map[Mode]ModelMapping{
			ModeNormal: {
				Main:         "google/gemini-2.5-flash",
				FileExplorer: "google/gemini-2.5-flash",
				SubAgent:     "google/gemini-2.5-flash",
			},
			ModeHeavy: {
				Main:         "anthropic/claude-sonnet-4",
				FileExplorer: "anthropic/claude-haiku-4-5",
				SubAgent:     "anthropic/claude-sonnet-4",
			},
			ModeMax: {
				Main:         "anthropic/claude-sonnet-4",
				FileExplorer: "anthropic/claude-sonnet-4",
				SubAgent:     "anthropic/claude-sonnet-4",
			},
		},
	})
}

func TestResolveModel(t *testing.T) {
	r := testResolver()

	tests := []struct {
		mode Mode
		role AgentRole
		want string
	}{
		{ModeNormal, RoleMain, "google/gemini-2.5-flash"},
		{ModeNormal, RoleFileExplorer, "google/gemini-2.5-flash"},
		{ModeNormal, RoleSubAgent, "google/gemini-2.5-flash"},
		{ModeHeavy, RoleMain, "anthropic/claude-sonnet-4"},
		{ModeHeavy, RoleFileExplorer, "anthropic/claude-haiku-4-5"},
		{ModeHeavy, RoleSubAgent, "anthropic/claude-sonnet-4"},
		{ModeMax, RoleMain, "anthropic/claude-sonnet-4"},
		{ModeMax, RoleFileExplorer, "anthropic/claude-sonnet-4"},
		{ModeMax, RoleSubAgent, "anthropic/claude-sonnet-4"},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode)+"/"+string(tt.role), func(t *testing.T) {
			got := r.ResolveModel(tt.mode, tt.role)
			if got != tt.want {
				t.Errorf("ResolveModel(%q, %q) = %q, want %q", tt.mode, tt.role, got, tt.want)
			}
		})
	}
}

func TestResolveModelForAgent(t *testing.T) {
	r := NewResolverFromConfig(ModelConfig{
		Modes: map[Mode]ModelMapping{
			ModeNormal: {
				Main:         "google/gemini-2.5-flash",
				FileExplorer: "google/gemini-2.5-flash",
				SubAgent:     "google/gemini-2.5-flash",
				AgentOverrides: map[string]string{
					"editor":  "anthropic/claude-sonnet-4",
					"thinker": "openai/o3",
				},
			},
			ModeHeavy: {
				Main:         "anthropic/claude-sonnet-4",
				FileExplorer: "anthropic/claude-haiku-4-5",
				SubAgent:     "anthropic/claude-sonnet-4",
			},
		},
	})

	// Agent with override should use the override
	got := r.ResolveModelForAgent(ModeNormal, RoleSubAgent, "editor")
	if got != "anthropic/claude-sonnet-4" {
		t.Errorf("ResolveModelForAgent(normal, sub_agent, editor) = %q, want anthropic/claude-sonnet-4", got)
	}

	// Another agent with override
	got = r.ResolveModelForAgent(ModeNormal, RoleSubAgent, "thinker")
	if got != "openai/o3" {
		t.Errorf("ResolveModelForAgent(normal, sub_agent, thinker) = %q, want openai/o3", got)
	}

	// Agent without override falls back to role default
	got = r.ResolveModelForAgent(ModeNormal, RoleSubAgent, "researcher")
	if got != "google/gemini-2.5-flash" {
		t.Errorf("ResolveModelForAgent(normal, sub_agent, researcher) = %q, want google/gemini-2.5-flash", got)
	}

	// Mode without overrides falls back to role default
	got = r.ResolveModelForAgent(ModeHeavy, RoleSubAgent, "editor")
	if got != "anthropic/claude-sonnet-4" {
		t.Errorf("ResolveModelForAgent(heavy, sub_agent, editor) = %q, want anthropic/claude-sonnet-4", got)
	}

	// Main role ignores agent overrides
	got = r.ResolveModelForAgent(ModeNormal, RoleMain, "base")
	if got != "google/gemini-2.5-flash" {
		t.Errorf("ResolveModelForAgent(normal, main, base) = %q, want google/gemini-2.5-flash", got)
	}

	// Empty agent ID falls back to role
	got = r.ResolveModelForAgent(ModeNormal, RoleSubAgent, "")
	if got != "google/gemini-2.5-flash" {
		t.Errorf("ResolveModelForAgent(normal, sub_agent, empty) = %q, want google/gemini-2.5-flash", got)
	}
}

func TestGetConfigDeepCopiesAgentOverrides(t *testing.T) {
	r := NewResolverFromConfig(ModelConfig{
		Modes: map[Mode]ModelMapping{
			ModeNormal: {
				Main:     "test/model",
				SubAgent: "test/sub",
				AgentOverrides: map[string]string{
					"editor": "test/editor",
				},
			},
		},
	})

	cfg := r.GetConfig()
	cfg.Modes[ModeNormal].AgentOverrides["editor"] = "mutated"

	got := r.ResolveModelForAgent(ModeNormal, RoleSubAgent, "editor")
	if got == "mutated" {
		t.Error("GetConfig did not deep-copy AgentOverrides")
	}
	if got != "test/editor" {
		t.Errorf("ResolveModelForAgent = %q, want test/editor", got)
	}
}

func TestLoadModelConfigWithAgentOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_config.yaml")
	yaml := `modes:
  normal:
    main: "model-a"
    file_explorer: "model-b"
    sub_agent: "model-c"
    agent_overrides:
      editor: "model-d"
      thinker: "model-e"
`
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := LoadModelConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Modes[ModeNormal].SubAgent != "model-c" {
		t.Errorf("normal.sub_agent = %q", cfg.Modes[ModeNormal].SubAgent)
	}
	if cfg.Modes[ModeNormal].AgentOverrides["editor"] != "model-d" {
		t.Errorf("normal.agent_overrides.editor = %q", cfg.Modes[ModeNormal].AgentOverrides["editor"])
	}
	if cfg.Modes[ModeNormal].AgentOverrides["thinker"] != "model-e" {
		t.Errorf("normal.agent_overrides.thinker = %q", cfg.Modes[ModeNormal].AgentOverrides["thinker"])
	}
}

func TestResolveModelUnknownModeFallsBack(t *testing.T) {
	r := testResolver()
	got := r.ResolveModel(Mode("unknown"), RoleMain)
	want := "google/gemini-2.5-flash" // ModeNormal fallback
	if got != want {
		t.Errorf("ResolveModel(unknown, main) = %q, want %q", got, want)
	}
}

func TestLoadModelConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_config.yaml")
	yaml := `modes:
  normal:
    main: "model-a"
    file_explorer: "model-b"
    sub_agent: "model-c"
  heavy:
    main: "model-d"
    file_explorer: "model-e"
    sub_agent: "model-f"
`
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := LoadModelConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Modes[ModeNormal].Main != "model-a" {
		t.Errorf("normal.main = %q", cfg.Modes[ModeNormal].Main)
	}
	if cfg.Modes[ModeHeavy].FileExplorer != "model-e" {
		t.Errorf("heavy.file_explorer = %q", cfg.Modes[ModeHeavy].FileExplorer)
	}
}

func TestNewResolver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_config.yaml")
	yaml := `modes:
  normal:
    main: "test/model"
    file_explorer: "test/model"
    sub_agent: "test/model"
`
	os.WriteFile(path, []byte(yaml), 0644)

	r, err := NewResolver(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r.ResolveModel(ModeNormal, RoleMain)
	if got != "test/model" {
		t.Errorf("ResolveModel = %q", got)
	}
}

func TestUpdateConfigPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_config.yaml")
	yaml := `modes:
  normal:
    main: "old/model"
    file_explorer: "old/model"
    sub_agent: "old/model"
`
	os.WriteFile(path, []byte(yaml), 0644)

	r, err := NewResolver(path)
	if err != nil {
		t.Fatal(err)
	}

	newCfg := ModelConfig{
		Modes: map[Mode]ModelMapping{
			ModeNormal: {Main: "new/model", FileExplorer: "new/fe", SubAgent: "new/sub"},
		},
	}
	if err := r.UpdateConfig(newCfg); err != nil {
		t.Fatal(err)
	}

	if got := r.ResolveModel(ModeNormal, RoleMain); got != "new/model" {
		t.Errorf("in-memory: ResolveModel = %q", got)
	}

	r2, err := NewResolver(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.ResolveModel(ModeNormal, RoleMain); got != "new/model" {
		t.Errorf("persisted: ResolveModel = %q", got)
	}
}

func TestGetConfigReturnsCopy(t *testing.T) {
	r := testResolver()
	cfg := r.GetConfig()

	cfg.Modes[ModeNormal] = ModelMapping{Main: "mutated"}

	got := r.ResolveModel(ModeNormal, RoleMain)
	if got == "mutated" {
		t.Error("GetConfig did not return a copy")
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"normal", ModeNormal},
		{"heavy", ModeHeavy},
		{"max", ModeMax},
		{"", ModeNormal},
		{"invalid", ModeNormal},
		{"NORMAL", ModeNormal}, // case-sensitive
		{"free", ModeNormal},  // old mode falls back
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseMode(tt.input)
			if got != tt.want {
				t.Errorf("ParseMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestModeValid(t *testing.T) {
	if !ModeNormal.Valid() {
		t.Error("ModeNormal.Valid() = false")
	}
	if !ModeHeavy.Valid() {
		t.Error("ModeHeavy.Valid() = false")
	}
	if !ModeMax.Valid() {
		t.Error("ModeMax.Valid() = false")
	}
	if Mode("bogus").Valid() {
		t.Error("Mode(bogus).Valid() = true")
	}
	if Mode("free").Valid() {
		t.Error("Mode(free).Valid() = true (removed)")
	}
}

func TestAllModes(t *testing.T) {
	modes := AllModes()
	if len(modes) != 3 {
		t.Fatalf("AllModes() returned %d modes, want 3", len(modes))
	}
	seen := make(map[Mode]bool)
	for _, m := range modes {
		seen[m] = true
	}
	for _, want := range []Mode{ModeNormal, ModeHeavy, ModeMax} {
		if !seen[want] {
			t.Errorf("AllModes() missing %q", want)
		}
	}
}

func TestModeString(t *testing.T) {
	if ModeNormal.String() != "normal" {
		t.Errorf("ModeNormal.String() = %q", ModeNormal.String())
	}
}
