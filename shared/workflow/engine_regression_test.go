package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type funcRunner struct {
	run func(ctx context.Context, agentID, task string) (string, error)
}

func (f funcRunner) RunAgent(ctx context.Context, agentID, task string) (string, error) {
	return f.run(ctx, agentID, task)
}

// A parallel block must not fan out to an unbounded number of concurrent agents.
func TestExecuteParallelBoundsConcurrency(t *testing.T) {
	var inFlight, peak int64
	runner := funcRunner{run: func(ctx context.Context, agentID, task string) (string, error) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		return "ok", nil
	}}

	steps := make([]Step, 20)
	for i := range steps {
		steps[i] = Step{Agent: "a", Task: "t"}
	}

	e := NewEngine()
	if err := e.executeParallel(context.Background(), 0, steps, EngineConfig{Runner: runner}); err != nil {
		t.Fatalf("executeParallel: %v", err)
	}
	if got := atomic.LoadInt64(&peak); got > int64(maxParallelSteps) {
		t.Fatalf("peak concurrency was %d, want at most %d", got, maxParallelSteps)
	}
	if got := atomic.LoadInt64(&peak); got < 2 {
		t.Fatalf("steps did not run concurrently at all (peak %d)", got)
	}
}

// Cancellation must stop steps that have not started and be reported as
// context.Canceled, not flattened into a string.
func TestExecuteParallelPropagatesCancellation(t *testing.T) {
	var started int64
	ctx, cancel := context.WithCancel(context.Background())
	runner := funcRunner{run: func(ctx context.Context, agentID, task string) (string, error) {
		atomic.AddInt64(&started, 1)
		cancel()
		<-ctx.Done()
		return "", ctx.Err()
	}}

	steps := make([]Step, 12)
	for i := range steps {
		steps[i] = Step{Agent: "a", Task: "t"}
	}

	e := NewEngine()
	err := e.executeParallel(ctx, 0, steps, EngineConfig{Runner: runner})
	cancel()

	if err == nil {
		t.Fatal("expected an error after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v does not unwrap to context.Canceled: step errors were flattened", err)
	}
	if got := atomic.LoadInt64(&started); got > int64(maxParallelSteps) {
		t.Fatalf("%d steps started after cancellation, want at most %d", got, maxParallelSteps)
	}
}

// A failing step's error must reach the caller intact.
func TestExecuteParallelSurfacesStepErrors(t *testing.T) {
	sentinel := errors.New("agent exploded")
	runner := funcRunner{run: func(_ context.Context, agentID, _ string) (string, error) {
		if agentID == "bad" {
			return "", sentinel
		}
		return "fine", nil
	}}

	e := NewEngine()
	err := e.executeParallel(context.Background(), 0, []Step{
		{Agent: "good", Task: "t", OutputVar: "good_out"},
		{Agent: "bad", Task: "t", OutputVar: "bad_out"},
	}, EngineConfig{Runner: runner})

	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap the step error", err)
	}
	vars := e.GetVars()
	if vars["good_out"] != "fine" {
		t.Fatalf("successful step output not stored: %v", vars)
	}
	if _, ok := vars["bad_out"]; ok {
		t.Fatalf("failed step stored an output variable: %v", vars)
	}
}

// OnEvent must not be invoked concurrently: callers (the TUI) are not required
// to be goroutine safe.
func TestExecuteParallelSerializesEvents(t *testing.T) {
	var mu sync.Mutex
	var concurrent, seen int
	var overlap bool

	runner := funcRunner{run: func(_ context.Context, _, _ string) (string, error) {
		time.Sleep(5 * time.Millisecond)
		return "ok", nil
	}}
	cfg := EngineConfig{
		Runner: runner,
		OnEvent: func(StepEvent) {
			mu.Lock()
			concurrent++
			if concurrent > 1 {
				overlap = true
			}
			seen++
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			concurrent--
			mu.Unlock()
		},
	}

	steps := make([]Step, 8)
	for i := range steps {
		steps[i] = Step{Agent: "a", Task: "t"}
	}
	e := NewEngine()
	if err := e.executeParallel(context.Background(), 0, steps, cfg); err != nil {
		t.Fatal(err)
	}
	if overlap {
		t.Fatal("OnEvent was called concurrently from parallel steps")
	}
	if seen == 0 {
		t.Fatal("no events emitted")
	}
}

// A step whose output is legitimately empty must still define its variable.
func TestExecuteParallelStoresEmptyOutput(t *testing.T) {
	runner := funcRunner{run: func(_ context.Context, _, _ string) (string, error) { return "", nil }}
	e := NewEngine()
	if err := e.executeParallel(context.Background(), 0, []Step{
		{Agent: "a", Task: "t", OutputVar: "out"},
	}, EngineConfig{Runner: runner}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.GetVars()["out"]; !ok {
		t.Fatal("an empty step output left the variable undefined")
	}
}

func TestEvaluateConditionUnresolvedVariable(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		vars      map[string]string
		want      bool
	}{
		{name: "defined not_empty", condition: "{{review}} not_empty", vars: map[string]string{"review": "text"}, want: true},
		{name: "undefined not_empty", condition: "{{review}} not_empty", vars: nil, want: false},
		{name: "undefined empty", condition: "{{review}} empty", vars: nil, want: true},
		{name: "defined empty", condition: "{{review}} empty", vars: map[string]string{"review": ""}, want: true},
		{name: "undefined contains", condition: "{{review}} contains 'NEEDS'", vars: nil, want: false},
		{name: "defined contains", condition: "{{review}} contains 'NEEDS'", vars: map[string]string{"review": "NEEDS_CHANGES"}, want: true},
		{name: "no condition runs", condition: "", vars: nil, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateCondition(tc.condition, tc.vars); got != tc.want {
				t.Fatalf("EvaluateCondition(%q) = %v, want %v", tc.condition, got, tc.want)
			}
		})
	}
}

func TestTruncateTaskIsRuneSafe(t *testing.T) {
	tests := []struct {
		name   string
		task   string
		maxLen int
	}{
		{name: "multi-byte", task: strings.Repeat("é", 50), maxLen: 10},
		{name: "tiny limit", task: "abcdef", maxLen: 2},
		{name: "zero limit", task: "abcdef", maxLen: 0},
		{name: "short input", task: "ok", maxLen: 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateTask(tc.task, tc.maxLen)
			if !utf8ValidString(got) {
				t.Fatalf("truncateTask produced invalid UTF-8: %q", got)
			}
		})
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
