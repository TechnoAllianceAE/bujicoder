package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// maxConcurrentTasks limits the number of parallel agent tasks to avoid
// exhausting API rate limits and memory.
const maxConcurrentTasks = 6

// maxCoordinatorTasks limits the total number of tasks the planner can generate.
const maxCoordinatorTasks = 20

// CoordinatorTask represents a single task in the goal decomposition.
type CoordinatorTask struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	AgentID     string   `json:"agent_id"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

// TaskStatus tracks the execution state of a coordinator task.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskBlocked    TaskStatus = "blocked"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
)

// TaskState holds the runtime state for a coordinator task.
type TaskState struct {
	Task   CoordinatorTask
	Status TaskStatus
	Result string
	Error  string
}

// CoordinatorResult holds the final result of a coordinated goal execution.
type CoordinatorResult struct {
	Goal       string
	Tasks      []TaskState
	FinalText  string
	TotalSteps int
}

// RunCoordinatedGoal takes a high-level goal, decomposes it into tasks using the
// planner agent, resolves dependencies, executes tasks with specialist agents,
// and synthesizes the final result.
func (r *Runtime) RunCoordinatedGoal(ctx context.Context, goal string, cfg RunConfig) (*CoordinatorResult, error) {
	result := &CoordinatorResult{Goal: goal}

	// Step 1: Decompose the goal into tasks using the planner.
	if cfg.OnEvent != nil {
		cfg.OnEvent(Event{
			Type:    EventStatus,
			AgentID: "coordinator",
			Text:    "Decomposing goal into tasks...",
		})
	}

	tasks, err := r.decomposeGoal(ctx, goal, cfg)
	if err != nil {
		return nil, fmt.Errorf("goal decomposition failed: %w", err)
	}

	if len(tasks) == 0 {
		return nil, fmt.Errorf("planner produced no tasks for goal: %s", goal)
	}

	// Enforce task count limit.
	if len(tasks) > maxCoordinatorTasks {
		tasks = tasks[:maxCoordinatorTasks]
	}

	// Build task ID set for dependency validation.
	taskIDs := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		taskIDs[t.ID] = true
	}

	// Initialize task states.
	states := make([]TaskState, len(tasks))
	stateMap := make(map[string]*TaskState) // id -> state
	for i, task := range tasks {
		// Validate depends_on references (#25).
		var validDeps []string
		for _, depID := range task.DependsOn {
			if taskIDs[depID] {
				validDeps = append(validDeps, depID)
			} else {
				r.log.Warn().Str("task", task.ID).Str("invalid_dep", depID).Msg("ignoring invalid dependency reference")
			}
		}
		tasks[i].DependsOn = validDeps
		task = tasks[i]

		status := TaskPending
		if len(task.DependsOn) > 0 {
			status = TaskBlocked
		}
		states[i] = TaskState{Task: task, Status: status}
		stateMap[task.ID] = &states[i]
	}

	// Cycle detection using Kahn's algorithm (#9).
	if err := detectCycles(tasks); err != nil {
		return nil, fmt.Errorf("invalid task graph: %w", err)
	}

	if cfg.OnEvent != nil {
		var taskList strings.Builder
		for _, t := range tasks {
			deps := ""
			if len(t.DependsOn) > 0 {
				deps = fmt.Sprintf(" (depends on: %s)", strings.Join(t.DependsOn, ", "))
			}
			taskList.WriteString(fmt.Sprintf("  [%s] %s → %s%s\n", t.ID, t.Title, t.AgentID, deps))
		}
		cfg.OnEvent(Event{
			Type:    EventStatus,
			AgentID: "coordinator",
			Text:    fmt.Sprintf("Task plan (%d tasks):\n%s", len(tasks), taskList.String()),
		})
	}

	// Step 2: Execute tasks respecting dependencies.
	// Use a simple loop: in each round, find all ready tasks (pending + deps satisfied),
	// run them in parallel, then unblock dependents.

	// Create shared memory for inter-task knowledge sharing.
	if cfg.SharedMemory == nil {
		cfg.SharedMemory = NewSharedMemory()
	}

	// Concurrency limiter (#24).
	sem := make(chan struct{}, maxConcurrentTasks)

	// Atomic counter for TotalSteps to avoid data race (#8).
	var totalSteps int64

	for {
		// Find ready tasks.
		var ready []*TaskState
		for i := range states {
			if states[i].Status == TaskPending {
				ready = append(ready, &states[i])
			}
		}

		if len(ready) == 0 {
			// Check if any tasks are still blocked.
			anyBlocked := false
			for _, s := range states {
				if s.Status == TaskBlocked {
					anyBlocked = true
					break
				}
			}
			if anyBlocked {
				// Deadlock or all dependencies failed — mark remaining as failed.
				for i := range states {
					if states[i].Status == TaskBlocked {
						states[i].Status = TaskFailed
						states[i].Error = "blocked: dependencies could not be satisfied (cycle or failed prerequisites)"
					}
				}
			}
			break
		}

		// Execute ready tasks in parallel with concurrency limit.
		var wg sync.WaitGroup
		for _, ts := range ready {
			ts.Status = TaskInProgress
			wg.Add(1)
			go func(ts *TaskState) {
				defer wg.Done()
				sem <- struct{}{}        // acquire
				defer func() { <-sem }() // release

				if cfg.OnEvent != nil {
					cfg.OnEvent(Event{
						Type:    EventStatus,
						AgentID: ts.Task.AgentID,
						Text:    fmt.Sprintf("Starting task [%s]: %s", ts.Task.ID, ts.Task.Title),
					})
				}

				// Build context with results from dependencies.
				// Safe to read from stateMap here: deps are from previous waves,
				// completed before this wave started (wg.Wait serializes waves).
				taskPrompt := ts.Task.Description
				if len(ts.Task.DependsOn) > 0 {
					var depContext strings.Builder
					depContext.WriteString("\n\n## Context from prerequisite tasks:\n\n")
					for _, depID := range ts.Task.DependsOn {
						if dep, ok := stateMap[depID]; ok && dep.Status == TaskCompleted {
							depResult := dep.Result
							if len(depResult) > 2000 {
								depResult = depResult[:2000] + "... [truncated]"
							}
							depContext.WriteString(fmt.Sprintf("### %s (task %s):\n%s\n\n", dep.Task.Title, dep.Task.ID, depResult))
						}
					}
					taskPrompt += depContext.String()
				}

				// Resolve the agent definition.
				agentDef, ok := r.agentRegistry.Get(ts.Task.AgentID)
				if !ok {
					ts.Status = TaskFailed
					ts.Error = fmt.Sprintf("agent %q not found", ts.Task.AgentID)
					return
				}

				if cfg.CostMode != "" && cfg.ModelResolver != nil {
					agentDef = agentDef.WithCostMode(cfg.CostMode, cfg.ModelResolver)
				}

				childCfg := RunConfig{
					AgentDef:      agentDef,
					UserMessage:   taskPrompt,
					ProjectRoot:   cfg.ProjectRoot,
					CostMode:      cfg.CostMode,
					ModelResolver: cfg.ModelResolver,
					SharedMemory:  cfg.SharedMemory,
					ContextCache:  cfg.ContextCache,
					LSPManager:    cfg.LSPManager,
					OnEvent: func(ev Event) {
						if cfg.OnEvent != nil {
							ev.AgentID = ts.Task.AgentID
							cfg.OnEvent(ev)
						}
					},
				}

				runResult, err := r.Run(ctx, childCfg)
				if err != nil {
					ts.Status = TaskFailed
					ts.Error = err.Error()
					return
				}

				ts.Status = TaskCompleted
				ts.Result = runResult.FinalText
				atomic.AddInt64(&totalSteps, int64(runResult.TotalSteps))

				// Store result in shared memory for other tasks.
				cfg.SharedMemory.Write(ts.Task.AgentID, "task_"+ts.Task.ID, runResult.FinalText)

				if cfg.OnEvent != nil {
					cfg.OnEvent(Event{
						Type:    EventStatus,
						AgentID: ts.Task.AgentID,
						Text:    fmt.Sprintf("Completed task [%s]: %s (%d steps)", ts.Task.ID, ts.Task.Title, runResult.TotalSteps),
					})
				}
			}(ts)
		}
		wg.Wait()

		// Unblock tasks whose dependencies are now satisfied.
		for i := range states {
			if states[i].Status != TaskBlocked {
				continue
			}
			allSatisfied := true
			anyFailed := false
			for _, depID := range states[i].Task.DependsOn {
				if dep, ok := stateMap[depID]; ok {
					if dep.Status == TaskFailed {
						anyFailed = true
						break
					}
					if dep.Status != TaskCompleted {
						allSatisfied = false
					}
				}
			}
			if anyFailed {
				// Cascade failure.
				states[i].Status = TaskFailed
				states[i].Error = "blocked: one or more dependencies failed"
			} else if allSatisfied {
				states[i].Status = TaskPending
			}
		}
	}

	result.TotalSteps = int(atomic.LoadInt64(&totalSteps))

	result.Tasks = states

	// Step 3: Synthesize final result.
	if cfg.OnEvent != nil {
		cfg.OnEvent(Event{
			Type:    EventStatus,
			AgentID: "coordinator",
			Text:    "Synthesizing final result...",
		})
	}

	finalText, err := r.synthesizeResult(ctx, goal, states, cfg)
	if err != nil {
		// Fall back to simple concatenation.
		var sb strings.Builder
		for _, ts := range states {
			sb.WriteString(fmt.Sprintf("## Task: %s [%s]\n\n", ts.Task.Title, ts.Status))
			if ts.Result != "" {
				sb.WriteString(ts.Result)
			}
			if ts.Error != "" {
				sb.WriteString(fmt.Sprintf("Error: %s", ts.Error))
			}
			sb.WriteString("\n\n")
		}
		result.FinalText = sb.String()
	} else {
		result.FinalText = finalText
	}

	return result, nil
}

// decomposeGoal uses the planner agent to break a goal into tasks.
func (r *Runtime) decomposeGoal(ctx context.Context, goal string, cfg RunConfig) ([]CoordinatorTask, error) {
	plannerDef, ok := r.agentRegistry.Get("planner")
	if !ok {
		return nil, fmt.Errorf("planner agent not found in registry")
	}

	if cfg.CostMode != "" && cfg.ModelResolver != nil {
		plannerDef = plannerDef.WithCostMode(cfg.CostMode, cfg.ModelResolver)
	}

	decompositionPrompt := fmt.Sprintf(`Decompose the following goal into a set of concrete tasks that can be assigned to specialist agents.

## Goal
%s

## Available Agents
- "researcher": Thorough investigation of code, docs, and web resources (read-only)
- "editor": Precise, targeted file editing and code changes
- "file_explorer": Fast, lightweight codebase navigation and file discovery
- "planner": Task decomposition into ordered steps
- "reviewer": Code review for correctness, security, and style
- "thinker": Deep reasoning about complex problems
- "git_committer": Git operations with proper commit messages

## Output Format
Return a JSON array of tasks. Each task has:
- "id": Short unique identifier (e.g. "t1", "t2")
- "title": Brief title for the task
- "description": Detailed description of what the agent should do
- "agent_id": Which agent to assign (must be one from the list above)
- "depends_on": Array of task IDs that must complete before this task can start (optional)

Rules:
- Order tasks logically — research/exploration before implementation
- Use depends_on to express real dependencies (don't make everything sequential)
- Independent tasks with no dependencies can run in parallel
- Keep tasks focused — each should be achievable by its assigned agent
- Include a final review task when code changes are involved

Return ONLY the JSON array, no markdown, no explanation.`, goal)

	plannerCfg := RunConfig{
		AgentDef:      plannerDef,
		UserMessage:   decompositionPrompt,
		ProjectRoot:   cfg.ProjectRoot,
		CostMode:      cfg.CostMode,
		ModelResolver: cfg.ModelResolver,
		ContextCache:  cfg.ContextCache,
		OnEvent: func(ev Event) {
			if cfg.OnEvent != nil {
				ev.AgentID = "planner"
				cfg.OnEvent(ev)
			}
		},
	}

	result, err := r.Run(ctx, plannerCfg)
	if err != nil {
		return nil, fmt.Errorf("planner failed: %w", err)
	}

	// Parse the JSON array from the planner's output.
	text := result.FinalText
	// Try to extract JSON from the response (may have markdown fences).
	text = extractJSON(text)

	var tasks []CoordinatorTask
	if err := json.Unmarshal([]byte(text), &tasks); err != nil {
		return nil, fmt.Errorf("failed to parse planner output as JSON: %w\nOutput: %s", err, result.FinalText)
	}

	// Validate tasks.
	validAgents := map[string]bool{
		"researcher": true, "editor": true, "file_explorer": true,
		"planner": true, "reviewer": true, "thinker": true, "git_committer": true,
	}

	for i, task := range tasks {
		if task.ID == "" {
			tasks[i].ID = fmt.Sprintf("t%d", i+1)
		}
		if !validAgents[task.AgentID] {
			// Default to researcher for unknown agents.
			tasks[i].AgentID = "researcher"
		}
	}

	return tasks, nil
}

// synthesizeResult uses a lightweight LLM call to combine task outputs into a coherent response.
func (r *Runtime) synthesizeResult(ctx context.Context, goal string, states []TaskState, cfg RunConfig) (string, error) {
	var taskOutputs strings.Builder
	taskOutputs.WriteString(fmt.Sprintf("# Goal\n%s\n\n# Task Results\n\n", goal))

	for _, ts := range states {
		taskOutputs.WriteString(fmt.Sprintf("## [%s] %s (agent: %s, status: %s)\n\n", ts.Task.ID, ts.Task.Title, ts.Task.AgentID, ts.Status))
		if ts.Result != "" {
			result := ts.Result
			if len(result) > 3000 {
				result = result[:3000] + "... [truncated]"
			}
			taskOutputs.WriteString(result + "\n\n")
		}
		if ts.Error != "" {
			taskOutputs.WriteString(fmt.Sprintf("Error: %s\n\n", ts.Error))
		}
	}

	// Use the base agent's model for synthesis.
	model := cfg.AgentDef.Model
	if base, ok := r.agentRegistry.Get("base"); ok {
		resolved := base
		if cfg.CostMode != "" && cfg.ModelResolver != nil {
			resolved = base.WithCostMode(cfg.CostMode, cfg.ModelResolver)
		}
		model = resolved.Model
	}

	provider, routedModel, err := r.llmRegistry.Route(model)
	if err != nil {
		return "", err
	}

	systemPrompt := `You are summarizing the results of a multi-agent task execution.
Synthesize the outputs from all tasks into a clear, concise final response that addresses the original goal.
Focus on what was accomplished, what changed, and any issues encountered.
Be concise and actionable.`

	maxTokens := 4096
	req := &llm.CompletionRequest{
		Model: routedModel,
		Messages: []llm.Message{
			{
				Role: "user",
				Content: []llm.ContentPart{
					{Type: "text", Text: taskOutputs.String()},
				},
			},
		},
		SystemPrompt: &systemPrompt,
		MaxTokens:    &maxTokens,
	}

	ch, err := provider.StreamCompletion(ctx, req)
	if err != nil {
		return "", err
	}

	var response strings.Builder
	for ev := range ch {
		if ev.Delta != nil {
			response.WriteString(ev.Delta.Text)
			if cfg.OnEvent != nil {
				cfg.OnEvent(Event{Type: EventDelta, Text: ev.Delta.Text, AgentID: "coordinator", Model: routedModel})
			}
		}
		if ev.Error != nil && !ev.Error.Retryable {
			return "", fmt.Errorf("synthesis error: %s", ev.Error.Message)
		}
	}

	return response.String(), nil
}

// detectCycles checks for cycles in the task dependency graph using Kahn's algorithm.
// Returns an error if cycles are detected.
func detectCycles(tasks []CoordinatorTask) error {
	// Build adjacency and in-degree.
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // dep -> tasks that depend on it

	for _, t := range tasks {
		if _, ok := inDegree[t.ID]; !ok {
			inDegree[t.ID] = 0
		}
		for _, dep := range t.DependsOn {
			inDegree[t.ID]++
			dependents[dep] = append(dependents[dep], t.ID)
		}
	}

	// Start with nodes that have no dependencies.
	var queue []string
	for _, t := range tasks {
		if inDegree[t.ID] == 0 {
			queue = append(queue, t.ID)
		}
	}

	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++

		for _, dep := range dependents[node] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if visited < len(tasks) {
		// Find the tasks involved in cycles for a useful error message.
		var cycleIDs []string
		for _, t := range tasks {
			if inDegree[t.ID] > 0 {
				cycleIDs = append(cycleIDs, t.ID)
			}
		}
		return fmt.Errorf("circular dependencies detected among tasks: %s", strings.Join(cycleIDs, ", "))
	}

	return nil
}

// extractJSON tries to extract a JSON array from text that may contain markdown fences.
func extractJSON(text string) string {
	text = strings.TrimSpace(text)

	// Try to find JSON within markdown code blocks.
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + len("```json")
		end := strings.Index(text[start:], "```")
		if end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + len("```")
		end := strings.Index(text[start:], "```")
		if end != -1 {
			candidate := strings.TrimSpace(text[start : start+end])
			if strings.HasPrefix(candidate, "[") {
				return candidate
			}
		}
	}

	// Try to find raw JSON array, respecting string literals.
	if idx := strings.Index(text, "["); idx != -1 {
		depth := 0
		inString := false
		escaped := false
		for i := idx; i < len(text); i++ {
			c := text[i]
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' && inString {
				escaped = true
				continue
			}
			if c == '"' {
				inString = !inString
				continue
			}
			if !inString {
				if c == '[' {
					depth++
				} else if c == ']' {
					depth--
					if depth == 0 {
						return text[idx : i+1]
					}
				}
			}
		}
	}

	return text
}
