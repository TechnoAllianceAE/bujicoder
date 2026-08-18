package agentruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// concurrentProvider answers every completion with a fixed text reply while
// tracking how many completions are in flight at the same time.
type concurrentProvider struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	hold     time.Duration
}

func (p *concurrentProvider) StreamCompletion(ctx context.Context, req *llm.CompletionRequest) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.inFlight++
	if p.inFlight > p.peak {
		p.peak = p.inFlight
	}
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent, 4)
	go func() {
		defer close(ch)
		select {
		case <-time.After(p.hold):
		case <-ctx.Done():
		}
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
		ch <- llm.StreamEvent{Delta: &llm.DeltaEvent{Text: "done"}}
		ch <- llm.StreamEvent{Complete: &llm.CompleteEvent{FinishReason: "stop"}}
	}()
	return ch, nil
}

func (p *concurrentProvider) Name() string { return "test" }

func (p *concurrentProvider) peakConcurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// spawnSetup registers n spawnable child agents and returns the runtime plus a
// parent RunConfig allowed to spawn them.
func spawnSetup(t *testing.T, provider llm.Provider, n int) (*Runtime, RunConfig, []spawnAgentSpec) {
	t.Helper()

	llmReg := llm.NewRegistry()
	llmReg.Register(provider)
	agentReg := agent.NewRegistry()

	specs := make([]spawnAgentSpec, 0, n)
	ids := make([]string, 0, n)
	for i := range n {
		id := fmt.Sprintf("child%d", i)
		agentReg.Register(&agent.Definition{ID: id, Model: "test/model", MaxSteps: 2})
		ids = append(ids, id)
		specs = append(specs, spawnAgentSpec{AgentID: id, Task: "do work"})
	}

	rt := New(llmReg, tools.NewRegistry(t.TempDir()), agentReg, zerolog.Nop())
	cfg := RunConfig{
		AgentDef: &agent.Definition{
			ID:              "base",
			Model:           "test/model",
			MaxSteps:        2,
			SpawnableAgents: ids,
		},
	}
	return rt, cfg, specs
}

func spawnArgs(specs []spawnAgentSpec) string {
	var sb strings.Builder
	sb.WriteString(`{"agents":[`)
	for i, s := range specs {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"agent_id":%q,"task":%q}`, s.AgentID, s.Task)
	}
	sb.WriteString("]}")
	return sb.String()
}

func TestSpawnAgentsRespectsConcurrencyCap(t *testing.T) {
	provider := &concurrentProvider{hold: 20 * time.Millisecond}
	rt, cfg, specs := spawnSetup(t, provider, maxConcurrentTasks*3)

	out, err := handleSpawnAgents(t.Context(), rt, spawnArgs(specs), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		if !strings.Contains(out, "=== Agent: "+s.AgentID+" ===") {
			t.Errorf("missing result for %s", s.AgentID)
		}
	}
	if peak := provider.peakConcurrency(); peak > maxConcurrentTasks {
		t.Errorf("peak concurrent sub-agents = %d, want <= %d", peak, maxConcurrentTasks)
	}
}

func TestSpawnAgentsSerializesParentEvents(t *testing.T) {
	provider := &concurrentProvider{hold: time.Millisecond}
	rt, cfg, specs := spawnSetup(t, provider, 8)

	// Deliberately unsynchronised, exactly like a TUI event collector: the
	// runtime must not invoke OnEvent from several goroutines at once.
	var events []Event
	cfg.OnEvent = func(ev Event) { events = append(events, ev) }

	if _, err := handleSpawnAgents(t.Context(), rt, spawnArgs(specs), cfg); err != nil {
		t.Fatal(err)
	}

	// Two status events per child at minimum (start + complete).
	if len(events) < len(specs)*2 {
		t.Errorf("collected %d events, want at least %d", len(events), len(specs)*2)
	}
}

func TestSpawnAgentsRejectsUnspawnableAgent(t *testing.T) {
	provider := &concurrentProvider{hold: time.Millisecond}
	rt, cfg, _ := spawnSetup(t, provider, 1)

	_, err := handleSpawnAgents(t.Context(), rt, `{"agents":[{"agent_id":"ghost","task":"x"}]}`, cfg)
	if err == nil {
		t.Fatal("expected error for agent outside the spawnable list")
	}
}

func TestDispatchPlanModeBlocksWrites(t *testing.T) {
	dir := t.TempDir()
	rt := setupRuntime(&testProvider{})
	agentDef := &agent.Definition{ID: "base", Model: "test/model", MaxSteps: 2, Tools: []string{"write_file"}}

	tests := []struct {
		name        string
		planMode    bool
		wantWritten bool
	}{
		{"plan mode blocks write_file", true, false},
		{"normal mode allows write_file", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := fmt.Sprintf("out-%v.go", tt.planMode)
			cfg := RunConfig{AgentDef: agentDef, ProjectRoot: dir, PlanMode: tt.planMode}
			calls := []llm.ToolCallEvent{{
				ID:            "c1",
				Name:          "write_file",
				ArgumentsJSON: fmt.Sprintf(`{"path":%q,"content":"package main"}`, name),
			}}

			results, err := dispatchToolCalls(t.Context(), rt, calls, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d results, want 1", len(results))
			}

			_, statErr := os.Stat(filepath.Join(dir, name))
			written := statErr == nil
			if written != tt.wantWritten {
				t.Fatalf("file written = %v, want %v (tool said: %q)", written, tt.wantWritten, results[0].Text)
			}
		})
	}
}
