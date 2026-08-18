package agentruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

func TestHashArgsIsCanonical(t *testing.T) {
	tests := []struct {
		name  string
		a, b  string
		equal bool
	}{
		{"whitespace only difference", `{"path":"a.go"}`, `{ "path" : "a.go" }`, true},
		{"key order difference", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"newline padding", `{"path":"a.go"}`, "{\n  \"path\": \"a.go\"\n}", true},
		{"different values", `{"path":"a.go"}`, `{"path":"b.go"}`, false},
		{"different nesting", `{"a":{"b":1}}`, `{"a":{"b":2}}`, false},
		{"non-JSON arguments compare verbatim", "not json", "not json", true},
		{"different non-JSON arguments", "not json", "other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hashArgs(tt.a) == hashArgs(tt.b); got != tt.equal {
				t.Fatalf("hashArgs(%q)==hashArgs(%q) = %v, want %v", tt.a, tt.b, got, tt.equal)
			}
		})
	}
}

// reformattingProvider repeats the same logical tool call forever, changing
// only its JSON formatting on each step.
type reformattingProvider struct{ calls int }

func (p *reformattingProvider) StreamCompletion(ctx context.Context, req *llm.CompletionRequest) (<-chan llm.StreamEvent, error) {
	args := `{"paths":["a.go"]}`
	if p.calls%2 == 1 {
		args = "{ \"paths\" :  [ \"a.go\" ] }"
	}
	p.calls++

	ch := make(chan llm.StreamEvent, 4)
	ch <- llm.StreamEvent{ToolCall: &llm.ToolCallEvent{ID: "c", Name: "read_files", ArgumentsJSON: args}}
	ch <- llm.StreamEvent{Complete: &llm.CompleteEvent{FinishReason: "tool_calls"}}
	close(ch)
	return ch, nil
}

func (p *reformattingProvider) Name() string { return "test" }

func TestLoopGuardSurvivesArgumentReformatting(t *testing.T) {
	rt := setupRuntime(&reformattingProvider{})

	result, err := rt.Run(t.Context(), RunConfig{
		AgentDef: &agent.Definition{
			ID:       "base",
			Model:    "test/model",
			MaxSteps: MaxIdenticalToolCalls + 4,
			Tools:    []string{"read_files"},
		},
		ProjectRoot: t.TempDir(),
		UserMessage: "read it",
	})
	if err != nil {
		t.Fatal(err)
	}

	tripped := false
	for _, msg := range result.Messages {
		for _, part := range msg.Content {
			if strings.Contains(part.Text, "[Loop Guard]") {
				tripped = true
			}
		}
	}
	if !tripped {
		t.Fatalf("loop guard never tripped after %d reformatted identical calls", result.TotalSteps)
	}
}

// leakyProvider emits a fatal error event and then keeps producing on an
// unbuffered channel. Its goroutine can only finish if the consumer drains the
// channel (or the stream context is cancelled).
type leakyProvider struct{ done chan struct{} }

func (p *leakyProvider) StreamCompletion(ctx context.Context, req *llm.CompletionRequest) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent) // unbuffered on purpose
	go func() {
		defer close(ch)
		defer close(p.done)
		ch <- llm.StreamEvent{Error: &llm.ErrorEvent{Code: "500", Message: "boom", Retryable: false}}
		for range 3 {
			select {
			case ch <- llm.StreamEvent{Delta: &llm.DeltaEvent{Text: "trailing"}}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (p *leakyProvider) Name() string { return "test" }

func TestProviderErrorDoesNotLeakStreamGoroutine(t *testing.T) {
	provider := &leakyProvider{done: make(chan struct{})}
	rt := setupRuntime(provider)

	_, err := rt.Run(t.Context(), RunConfig{
		AgentDef:    &agent.Definition{ID: "base", Model: "test/model", MaxSteps: 2},
		UserMessage: "hi",
	})
	if err == nil {
		t.Fatal("expected the provider error to fail the run")
	}

	select {
	case <-provider.done:
	case <-time.After(2 * time.Second):
		t.Fatal("provider stream goroutine is still blocked: the event channel was abandoned, leaking the goroutine and its response body")
	}
}
