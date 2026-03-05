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
model: openai/gpt-oss-120b:free
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
	_ = os.WriteFile(path, []byte(yaml), 0644)

	def, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if def.ID != "test-agent" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.Model != "openai/gpt-oss-120b:free" {
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
	_ = os.WriteFile(path, []byte(yaml), 0644)

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
	_ = os.WriteFile(path, []byte(yaml), 0644)

	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestLoadFileNoModel(t *testing.T) {
	dir := t.TempDir()
	yaml := `id: no-model
`
	path := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(path, []byte(yaml), 0644)

	def, err := LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Model != "" {
		t.Errorf("Model = %q, want empty", def.Model)
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
		_ = os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(yaml), 0644)
	}

	// Write a non-yaml file that should be skipped
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0644)

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
				Main:         "openai/gpt-oss-120b:free",
				FileExplorer: "google/gemma-3n-e2b-it:free",
				SubAgent:     "openai/gpt-oss-120b:free",
			},
			costmode.ModeMax: {
				Main:         "openai/gpt-oss-120b:free",
				FileExplorer: "openai/gpt-oss-120b:free",
				SubAgent:     "openai/gpt-oss-120b:free",
			},
		},
	})

	base := &Definition{ID: "base", Model: "openai/gpt-oss-120b:free"}
	normal := base.WithCostMode(costmode.ModeNormal, resolver)

	if base.Model != "openai/gpt-oss-120b:free" {
		t.Errorf("original modified: Model = %q", base.Model)
	}

	if normal.Model != "google/gemini-2.5-flash" {
		t.Errorf("normal base Model = %q, want google/gemini-2.5-flash", normal.Model)
	}

	fe := &Definition{ID: "file_explorer", Model: "google/gemma-3n-e2b-it:free"}
	feMax := fe.WithCostMode(costmode.ModeMax, resolver)
	if feMax.Model != "openai/gpt-oss-120b:free" {
		t.Errorf("max file_explorer Model = %q", feMax.Model)
	}

	sub := &Definition{ID: "researcher", Model: "openai/gpt-oss-120b:free"}
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
					"editor":  "openai/gpt-oss-120b:free",
					"thinker": "openai/o3",
				},
			},
		},
	})

	// Sub-agent with override should use the override model
	editor := &Definition{ID: "editor", Model: "default/model"}
	resolved := editor.WithCostMode(costmode.ModeNormal, resolver)
	if resolved.Model != "openai/gpt-oss-120b:free" {
		t.Errorf("editor override Model = %q, want openai/gpt-oss-120b:free", resolved.Model)
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
	base := &Definition{ID: "base", Model: "openai/gpt-oss-120b:free"}
	result := base.WithCostMode(costmode.ModeNormal, nil)

	if result.Model != "openai/gpt-oss-120b:free" {
		t.Errorf("nil resolver should not change model, got %q", result.Model)
	}
}
