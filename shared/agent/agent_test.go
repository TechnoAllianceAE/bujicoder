package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
)

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	yaml := `id: test-agent
version: "1.0.0"
display_name: "Test Agent"
publisher: test
model: anthropic/claude-sonnet-4
tools:
  - read_files
  - write_file
spawnable_agents:
  - sub1
max_steps: 25
max_tokens: 4096
system_prompt: "You are a test agent."
`
	path := filepath.Join(dir, "test.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	def, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if def.ID != "test-agent" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("Model = %q", def.Model)
	}
	if len(def.Tools) != 2 {
		t.Errorf("Tools = %v", def.Tools)
	}
	if len(def.SpawnableAgents) != 1 {
		t.Errorf("SpawnableAgents = %v", def.SpawnableAgents)
	}
	if def.MaxSteps != 25 {
		t.Errorf("MaxSteps = %d", def.MaxSteps)
	}
	if def.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d", def.MaxTokens)
	}
}

func TestLoadFileDefaults(t *testing.T) {
	dir := t.TempDir()
	yaml := `id: minimal
model: openai/gpt-4
`
	path := filepath.Join(dir, "minimal.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	def, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if def.OutputMode != "last_message" {
		t.Errorf("OutputMode = %q, want %q", def.OutputMode, "last_message")
	}
	if def.MaxSteps != 50 {
		t.Errorf("MaxSteps = %d, want 50", def.MaxSteps)
	}
	if def.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", def.MaxTokens)
	}
}

func TestLoadFileMissingID(t *testing.T) {
	dir := t.TempDir()
	yaml := `model: openai/gpt-4
`
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestLoadFileMissingModel(t *testing.T) {
	dir := t.TempDir()
	yaml := `id: no-model
`
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()

	def := &Definition{ID: "agent-1", Model: "test/model"}
	reg.Register(def)

	got, ok := reg.Get("agent-1")
	if !ok {
		t.Fatal("expected to find agent-1")
	}
	if got.ID != "agent-1" {
		t.Errorf("ID = %q", got.ID)
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Fatal("expected not to find nonexistent")
	}

	list := reg.List()
	if len(list) != 1 {
		t.Errorf("List() length = %d, want 1", len(list))
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()

	// Write two agent files
	for _, id := range []string{"alpha", "beta"} {
		yaml := "id: " + id + "\nmodel: test/model\n"
		os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(yaml), 0644)
	}

	// Write a non-yaml file that should be skipped
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0644)

	reg := NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatal(err)
	}

	if _, ok := reg.Get("alpha"); !ok {
		t.Error("missing alpha")
	}
	if _, ok := reg.Get("beta"); !ok {
		t.Error("missing beta")
	}
	if len(reg.List()) != 2 {
		t.Errorf("List() length = %d, want 2", len(reg.List()))
	}
}

func TestWithCostMode(t *testing.T) {
	resolver := costmode.NewResolverFromConfig(costmode.ModelConfig{
		Modes: map[costmode.Mode]costmode.ModelMapping{
			costmode.ModeNormal: {
				Main:         "google/gemini-2.5-flash",
				FileExplorer: "google/gemini-2.5-flash",
				SubAgent:     "google/gemini-2.5-flash",
			},
			costmode.ModeHeavy: {
				Main:         "anthropic/claude-sonnet-4",
				FileExplorer: "anthropic/claude-haiku-4-5",
				SubAgent:     "anthropic/claude-sonnet-4",
			},
			costmode.ModeMax: {
				Main:         "anthropic/claude-sonnet-4",
				FileExplorer: "anthropic/claude-sonnet-4",
				SubAgent:     "anthropic/claude-sonnet-4",
			},
		},
	})

	base := &Definition{ID: "base", Model: "anthropic/claude-sonnet-4"}
	normal := base.WithCostMode(costmode.ModeNormal, resolver)

	if base.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("original modified: Model = %q", base.Model)
	}

	if normal.Model != "google/gemini-2.5-flash" {
		t.Errorf("normal base Model = %q, want google/gemini-2.5-flash", normal.Model)
	}

	fe := &Definition{ID: "file_explorer", Model: "anthropic/claude-haiku-4-5"}
	feMax := fe.WithCostMode(costmode.ModeMax, resolver)
	if feMax.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("max file_explorer Model = %q", feMax.Model)
	}

	sub := &Definition{ID: "researcher", Model: "anthropic/claude-sonnet-4"}
	subNormal := sub.WithCostMode(costmode.ModeNormal, resolver)
	if subNormal.Model != "google/gemini-2.5-flash" {
		t.Errorf("normal sub-agent Model = %q", subNormal.Model)
	}
}

func TestWithCostModeAgentOverrides(t *testing.T) {
	resolver := costmode.NewResolverFromConfig(costmode.ModelConfig{
		Modes: map[costmode.Mode]costmode.ModelMapping{
			costmode.ModeNormal: {
				Main:         "google/gemini-2.5-flash",
				FileExplorer: "google/gemini-2.5-flash",
				SubAgent:     "google/gemini-2.5-flash",
				AgentOverrides: map[string]string{
					"editor":  "anthropic/claude-sonnet-4",
					"thinker": "openai/o3",
				},
			},
		},
	})

	// Sub-agent with override should use the override model
	editor := &Definition{ID: "editor", Model: "default/model"}
	resolved := editor.WithCostMode(costmode.ModeNormal, resolver)
	if resolved.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("editor override Model = %q, want anthropic/claude-sonnet-4", resolved.Model)
	}

	// Sub-agent without override uses sub_agent default
	researcher := &Definition{ID: "researcher", Model: "default/model"}
	resolved = researcher.WithCostMode(costmode.ModeNormal, resolver)
	if resolved.Model != "google/gemini-2.5-flash" {
		t.Errorf("researcher (no override) Model = %q, want google/gemini-2.5-flash", resolved.Model)
	}

	// Main agent is NOT affected by agent_overrides
	base := &Definition{ID: "base", Model: "default/model"}
	resolved = base.WithCostMode(costmode.ModeNormal, resolver)
	if resolved.Model != "google/gemini-2.5-flash" {
		t.Errorf("base (main role) Model = %q, want google/gemini-2.5-flash", resolved.Model)
	}

	// Original should not be modified
	if editor.Model != "default/model" {
		t.Errorf("original modified: Model = %q", editor.Model)
	}
}

func TestWithCostModeNilResolver(t *testing.T) {
	base := &Definition{ID: "base", Model: "anthropic/claude-sonnet-4"}
	result := base.WithCostMode(costmode.ModeNormal, nil)

	if result.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("nil resolver should not change model, got %q", result.Model)
	}
}
