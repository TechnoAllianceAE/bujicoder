package costmode

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func testConfig() ModelConfig {
	return ModelConfig{Modes: map[Mode]ModelMapping{
		ModeNormal: {
			Main:           "vendor/main",
			FileExplorer:   "vendor/explorer",
			SubAgent:       "vendor/sub",
			AgentOverrides: map[string]string{"reviewer": "vendor/reviewer"},
		},
	}}
}

// A Resolver is read by every running agent. It must not share maps with the
// caller that supplied the config, otherwise a later mutation by that caller is
// a concurrent map read/write against live resolution (fatal, not recoverable).
func TestResolverDoesNotAliasCallerConfig(t *testing.T) {
	t.Run("NewResolverFromConfig", func(t *testing.T) {
		cfg := testConfig()
		r := NewResolverFromConfig(cfg)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 500 {
				cfg.Modes[ModeHeavy] = ModelMapping{Main: "vendor/other"}
				cfg.Modes[ModeNormal].AgentOverrides["editor"] = "vendor/editor"
			}
		}()
		go func() {
			defer wg.Done()
			for range 500 {
				_ = r.ResolveModelForAgent(ModeNormal, RoleSubAgent, "reviewer")
			}
		}()
		wg.Wait()

		if got := r.ResolveModelForAgent(ModeNormal, RoleSubAgent, "editor"); got != "vendor/sub" {
			t.Errorf("resolver picked up a caller-side mutation: %q", got)
		}
	})

	t.Run("UpdateConfig", func(t *testing.T) {
		r := NewResolverFromConfig(testConfig())
		cfg := testConfig()
		if err := r.UpdateConfig(cfg); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 500 {
				cfg.Modes[ModeMax] = ModelMapping{Main: "vendor/max"}
				cfg.Modes[ModeNormal].AgentOverrides["editor"] = "vendor/editor"
			}
		}()
		go func() {
			defer wg.Done()
			for range 500 {
				_ = r.ResolveModelForAgent(ModeMax, RoleMain, "")
			}
		}()
		wg.Wait()

		if got := r.ResolveModelForAgent(ModeMax, RoleMain, ""); got != "vendor/main" {
			t.Errorf("resolver picked up a caller-side mutation: %q", got)
		}
	})
}

func TestResolveModelFallbacks(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ModelConfig
		mode    Mode
		role    AgentRole
		agentID string
		want    string
	}{
		{
			name:    "agent override wins",
			cfg:     testConfig(),
			mode:    ModeNormal,
			role:    RoleSubAgent,
			agentID: "reviewer",
			want:    "vendor/reviewer",
		},
		{
			name:    "unknown mode falls back to normal",
			cfg:     testConfig(),
			mode:    Mode("bogus"),
			role:    RoleFileExplorer,
			agentID: "file_explorer",
			want:    "vendor/explorer",
		},
		{
			name:    "empty role entry falls back to the default model",
			cfg:     ModelConfig{Modes: map[Mode]ModelMapping{ModeNormal: {Main: "vendor/main"}}},
			mode:    ModeNormal,
			role:    RoleSubAgent,
			agentID: "researcher",
			want:    DefaultFallbackModel,
		},
		{
			name:    "empty override entry falls back to the role model",
			cfg:     ModelConfig{Modes: map[Mode]ModelMapping{ModeNormal: {SubAgent: "vendor/sub", AgentOverrides: map[string]string{"researcher": ""}}}},
			mode:    ModeNormal,
			role:    RoleSubAgent,
			agentID: "researcher",
			want:    "vendor/sub",
		},
		{
			name:    "empty config never yields an empty model",
			cfg:     ModelConfig{},
			mode:    ModeHeavy,
			role:    RoleMain,
			agentID: "base",
			want:    DefaultFallbackModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewResolverFromConfig(tt.cfg)
			if got := r.ResolveModelForAgent(tt.mode, tt.role, tt.agentID); got != tt.want {
				t.Errorf("ResolveModelForAgent = %q, want %q", got, tt.want)
			}
		})
	}
}

// UpdateConfig must never leave a truncated config on disk: another buji
// process (or the next start-up) reads this file directly.
func TestUpdateConfigWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_config.yaml")

	// A config big enough that a non-atomic write has a wide truncation window.
	big := ModelConfig{Modes: map[Mode]ModelMapping{}}
	overrides := make(map[string]string, 2000)
	for i := range 2000 {
		overrides[strings.Repeat("a", 40)+string(rune('A'+i%26))+strconv.Itoa(i)] = "vendor/model-" + strconv.Itoa(i)
	}
	big.Modes[ModeNormal] = ModelMapping{Main: "vendor/main", SubAgent: "vendor/sub", AgentOverrides: overrides}

	if err := saveModelConfig(path, &big); err != nil {
		t.Fatal(err)
	}
	r, err := NewResolver(path)
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var readErr error
	var readMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			cfg, err := LoadModelConfig(path)
			if err != nil {
				readMu.Lock()
				readErr = err
				readMu.Unlock()
				return
			}
			if m, ok := cfg.Modes[ModeNormal]; !ok || m.Main != "vendor/main" {
				readMu.Lock()
				readErr = errors.New("config missing modes.normal.main")
				readMu.Unlock()
				return
			}
		}
	}()

	for range 30 {
		if err := r.UpdateConfig(big); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	readMu.Lock()
	defer readMu.Unlock()
	if readErr != nil {
		t.Fatalf("concurrent reader observed a partially written config: %v", readErr)
	}

	// No temp files may be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "model_config.yaml" {
			t.Errorf("leftover file after atomic write: %s", e.Name())
		}
	}
}
