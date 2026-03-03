package agentruntime

import (
	"context"
	"testing"

	"github.com/rs/zerolog"

	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// testProvider is a mock LLM provider for testing.
type testProvider struct {
	responses [][]llm.StreamEvent // one response per call
	callIndex int
}

func (p *testProvider) StreamCompletion(ctx context.Context, req *llm.CompletionRequest) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 64)
	idx := p.callIndex
	if idx >= len(p.responses) {
		idx = len(p.responses) - 1
	}
	p.callIndex++
	go func() {
		for _, ev := range p.responses[idx] {
			ch <- ev
		}
		close(ch)
	}()
	return ch, nil
}

func (p *testProvider) Name() string { return "test" }

func setupRuntime(provider llm.Provider) *Runtime {
	llmReg := llm.NewRegistry()
	llmReg.Register(provider)

	toolReg := tools.NewRegistry("/tmp")
	agentReg := agent.NewRegistry()
	log := zerolog.Nop()

	return New(llmReg, toolReg, agentReg, log)
}

func TestRunNilAgent(t *testing.T) {
	rt := setupRuntime(&testProvider{})
	_, err := rt.Run(context.Background(), RunConfig{})
	if err == nil {
		t.Fatal("expected error for nil agent")
	}
}

func TestRunSimpleTextResponse(t *testing.T) {
	provider := &testProvider{
		responses: [][]llm.StreamEvent{
			{
				{Delta: &llm.DeltaEvent{Text: "Hello "}},
				{Delta: &llm.DeltaEvent{Text: "world!"}},
				{Complete: &llm.CompleteEvent{FinishReason: "stop"}},
			},
		},
	}

	rt := setupRuntime(provider)

	agentDef := &agent.Definition{
		ID:       "test-agent",
		Model:    "test/model",
		MaxSteps: 10,
	}

	result, err := rt.Run(context.Background(), RunConfig{
		AgentDef:    agentDef,
		UserMessage: "Hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.FinalText != "Hello world!" {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "Hello world!")
	}
	if result.TotalSteps != 1 {
		t.Errorf("TotalSteps = %d, want 1", result.TotalSteps)
	}
	if result.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "stop")
	}
}

func TestRunCancelledContext(t *testing.T) {
	provider := &testProvider{
		responses: [][]llm.StreamEvent{
			{
				{Delta: &llm.DeltaEvent{Text: "text"}},
				{Complete: &llm.CompleteEvent{FinishReason: "stop"}},
			},
		},
	}

	rt := setupRuntime(provider)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	agentDef := &agent.Definition{
		ID:       "test-agent",
		Model:    "test/model",
		MaxSteps: 10,
	}

	result, err := rt.Run(ctx, RunConfig{
		AgentDef:    agentDef,
		UserMessage: "Hello",
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if result.FinishReason != "cancelled" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "cancelled")
	}
}

func TestRunEventCallbacks(t *testing.T) {
	provider := &testProvider{
		responses: [][]llm.StreamEvent{
			{
				{Delta: &llm.DeltaEvent{Text: "response"}},
				{Complete: &llm.CompleteEvent{FinishReason: "stop"}},
			},
		},
	}

	rt := setupRuntime(provider)

	agentDef := &agent.Definition{
		ID:       "test-agent",
		Model:    "test/model",
		MaxSteps: 10,
	}

	var events []Event
	_, err := rt.Run(context.Background(), RunConfig{
		AgentDef:    agentDef,
		UserMessage: "Hello",
		OnEvent: func(ev Event) {
			events = append(events, ev)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should have: step_start, delta, step_end
	hasStepStart := false
	hasDelta := false
	hasStepEnd := false
	for _, ev := range events {
		switch ev.Type {
		case EventStepStart:
			hasStepStart = true
		case EventDelta:
			hasDelta = true
		case EventStepEnd:
			hasStepEnd = true
		}
	}
	if !hasStepStart {
		t.Error("missing step_start event")
	}
	if !hasDelta {
		t.Error("missing delta event")
	}
	if !hasStepEnd {
		t.Error("missing step_end event")
	}
}

func TestRunMaxSteps(t *testing.T) {
	// Provider always returns a tool call so the loop never naturally ends
	provider := &testProvider{
		responses: [][]llm.StreamEvent{
			{
				{ToolCall: &llm.ToolCallEvent{
					ID:            "call-1",
					Name:          "read_files",
					ArgumentsJSON: `{"paths":["/tmp/test.txt"]}`,
				}},
				{Complete: &llm.CompleteEvent{FinishReason: "tool_calls"}},
			},
		},
	}

	rt := setupRuntime(provider)

	agentDef := &agent.Definition{
		ID:       "test-agent",
		Model:    "test/model",
		MaxSteps: 2,
		Tools:    []string{"read_files"},
	}

	result, err := rt.Run(context.Background(), RunConfig{
		AgentDef:    agentDef,
		UserMessage: "Read something",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "max_steps" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "max_steps")
	}
	if result.TotalSteps != 2 {
		t.Errorf("TotalSteps = %d, want 2", result.TotalSteps)
	}
}
