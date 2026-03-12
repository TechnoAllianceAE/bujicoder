package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// AgentRunner is the interface the engine uses to execute a single agent.
// This decouples the workflow engine from the agent runtime implementation.
type AgentRunner interface {
	// RunAgent executes an agent with the given task and returns the output text.
	RunAgent(ctx context.Context, agentID, task string) (string, error)
}

// ApprovalFunc is a callback to ask the user for approval before running a step.
// It receives the step description and returns true if approved.
type ApprovalFunc func(stepDescription string) (bool, error)

// StepEvent is emitted during workflow execution for progress tracking.
type StepEvent struct {
	StepIndex   int
	AgentID     string
	Task        string
	Status      string // "start", "complete", "skip", "error", "approval_needed", "denied"
	Output      string
	Error       error
	IsParallel  bool
}

// OnStepEvent is a callback for workflow step events.
type OnStepEvent func(StepEvent)

// EngineConfig configures workflow execution.
type EngineConfig struct {
	Runner      AgentRunner
	ApprovalFn  ApprovalFunc
	OnEvent     OnStepEvent
	UserTask    string            // The original user task (set as {{user_task}} variable)
	InitialVars map[string]string // Initial variables
}

// Engine executes workflow pipelines.
type Engine struct {
	vars map[string]string
}

// NewEngine creates a new workflow engine.
func NewEngine() *Engine {
	return &Engine{
		vars: make(map[string]string),
	}
}

// Execute runs a workflow to completion.
func (e *Engine) Execute(ctx context.Context, wf *Workflow, cfg EngineConfig) error {
	if cfg.Runner == nil {
		return fmt.Errorf("agent runner is required")
	}

	// Initialize variables.
	e.vars = make(map[string]string)
	if cfg.InitialVars != nil {
		for k, v := range cfg.InitialVars {
			e.vars[k] = v
		}
	}
	if cfg.UserTask != "" {
		e.vars["user_task"] = cfg.UserTask
	}

	for i, step := range wf.Steps {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := e.executeStep(ctx, i, step, cfg); err != nil {
			return fmt.Errorf("workflow %q step %d (%s): %w", wf.ID, i, step.Agent, err)
		}
	}

	return nil
}

// GetVars returns a copy of the current variables.
func (e *Engine) GetVars() map[string]string {
	result := make(map[string]string, len(e.vars))
	for k, v := range e.vars {
		result[k] = v
	}
	return result
}

// executeStep executes a single workflow step.
func (e *Engine) executeStep(ctx context.Context, idx int, step Step, cfg EngineConfig) error {
	// Handle parallel steps.
	if len(step.Parallel) > 0 {
		return e.executeParallel(ctx, idx, step.Parallel, cfg)
	}

	// Evaluate condition.
	if !EvaluateCondition(step.Condition, e.vars) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(StepEvent{
				StepIndex: idx,
				AgentID:   step.Agent,
				Status:    "skip",
			})
		}
		return nil
	}

	// Interpolate the task template.
	task := Interpolate(step.Task, e.vars)

	// Check approval.
	if step.RequireApproval {
		if cfg.OnEvent != nil {
			cfg.OnEvent(StepEvent{
				StepIndex: idx,
				AgentID:   step.Agent,
				Task:      task,
				Status:    "approval_needed",
			})
		}
		if cfg.ApprovalFn != nil {
			approved, err := cfg.ApprovalFn(fmt.Sprintf("Step %d: %s → %s", idx+1, step.Agent, truncateTask(task, 100)))
			if err != nil {
				return fmt.Errorf("approval error: %w", err)
			}
			if !approved {
				if cfg.OnEvent != nil {
					cfg.OnEvent(StepEvent{
						StepIndex: idx,
						AgentID:   step.Agent,
						Status:    "denied",
					})
				}
				return fmt.Errorf("step %d denied by user", idx+1)
			}
		}
	}

	// Emit start event.
	if cfg.OnEvent != nil {
		cfg.OnEvent(StepEvent{
			StepIndex: idx,
			AgentID:   step.Agent,
			Task:      task,
			Status:    "start",
		})
	}

	// Execute the agent.
	output, err := cfg.Runner.RunAgent(ctx, step.Agent, task)
	if err != nil {
		if cfg.OnEvent != nil {
			cfg.OnEvent(StepEvent{
				StepIndex: idx,
				AgentID:   step.Agent,
				Status:    "error",
				Error:     err,
			})
		}
		return err
	}

	// Store output variable.
	if step.OutputVar != "" {
		e.vars[step.OutputVar] = output
	}

	// Emit complete event.
	if cfg.OnEvent != nil {
		cfg.OnEvent(StepEvent{
			StepIndex: idx,
			AgentID:   step.Agent,
			Task:      task,
			Status:    "complete",
			Output:    output,
		})
	}

	return nil
}

// executeParallel runs multiple steps concurrently and collects their results.
func (e *Engine) executeParallel(ctx context.Context, idx int, steps []Step, cfg EngineConfig) error {
	type result struct {
		stepIdx   int
		outputVar string
		output    string
		err       error
	}

	results := make([]result, len(steps))
	var wg sync.WaitGroup

	for i, step := range steps {
		wg.Add(1)
		go func(i int, step Step) {
			defer wg.Done()

			// Evaluate condition.
			if !EvaluateCondition(step.Condition, e.vars) {
				if cfg.OnEvent != nil {
					cfg.OnEvent(StepEvent{
						StepIndex:  idx,
						AgentID:    step.Agent,
						Status:     "skip",
						IsParallel: true,
					})
				}
				return
			}

			task := Interpolate(step.Task, e.vars)

			if cfg.OnEvent != nil {
				cfg.OnEvent(StepEvent{
					StepIndex:  idx,
					AgentID:    step.Agent,
					Task:       task,
					Status:     "start",
					IsParallel: true,
				})
			}

			output, err := cfg.Runner.RunAgent(ctx, step.Agent, task)
			results[i] = result{
				stepIdx:   i,
				outputVar: step.OutputVar,
				output:    output,
				err:       err,
			}

			status := "complete"
			if err != nil {
				status = "error"
			}
			if cfg.OnEvent != nil {
				cfg.OnEvent(StepEvent{
					StepIndex:  idx,
					AgentID:    step.Agent,
					Task:       task,
					Status:     status,
					Output:     output,
					Error:      err,
					IsParallel: true,
				})
			}
		}(i, step)
	}

	wg.Wait()

	// Store output variables and check for errors.
	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err.Error())
		}
		if r.outputVar != "" && r.output != "" {
			e.vars[r.outputVar] = r.output
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("parallel step errors: %s", strings.Join(errs, "; "))
	}

	return nil
}

// truncateTask truncates a task string for display.
func truncateTask(task string, maxLen int) string {
	task = strings.ReplaceAll(task, "\n", " ")
	if len(task) <= maxLen {
		return task
	}
	return task[:maxLen-3] + "..."
}
