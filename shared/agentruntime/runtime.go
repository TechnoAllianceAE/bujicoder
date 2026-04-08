// Package agentruntime implements the core agent orchestration engine that
// coordinates LLM completions, tool execution, and sub-agent spawning.
package agentruntime

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/contextcache"
	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
	"github.com/TechnoAllianceAE/bujicoder/shared/lsp"
	"github.com/TechnoAllianceAE/bujicoder/shared/snapshot"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// EventType identifies the type of runtime event.
type EventType string

const (
	EventDelta      EventType = "delta"
	EventToolCall   EventType = "tool_call"
	EventToolResult EventType = "tool_result"
	EventComplete   EventType = "complete"
	EventError      EventType = "error"
	EventStepStart  EventType = "step_start"
	EventStepEnd    EventType = "step_end"
	EventStatus     EventType = "status"
	EventCompact    EventType = "compact"
)

// Loop guard constants
const (
	MaxIdenticalToolCalls = 10     // Allow up to 10 identical tool calls before breaking
	MaxRunInputTokens     = 200000 // 200K input token budget per agent run
)

// Event is a runtime event emitted during an agent run.
type Event struct {
	Type       EventType
	Text       string
	ToolCallID string
	ToolName   string
	ArgsJSON   string
	IsError    bool
	StepNumber int
	Usage      *llm.UsageInfo
	AgentID    string // identifies which sub-agent emitted this event
	Model      string // model ID used for this step (e.g. "anthropic/claude-sonnet-4")
}

// OnEvent is a callback invoked for each runtime event.
type OnEvent func(Event)

// RunConfig configures a single agent run.
type RunConfig struct {
	AgentDef          *agent.Definition
	UserMessage       string
	UserImages        []llm.ContentPart      // Optional image_url parts to include in the user message
	History           []llm.Message           // Prior conversation history
	AncestorIDs       []string                // Parent run IDs for sub-agent tracking
	ProjectRoot       string                  // Working directory for dynamic context (file tree, git, knowledge files)
	OnEvent           OnEvent
	CostMode          costmode.Mode           // Cost mode for model selection (propagated to sub-agents)
	ModelResolver     *costmode.Resolver      // Server-side model resolver (propagated to sub-agents)
	ProposalCollector *tools.ProposalCollector    // When set, proposal tools accumulate here instead of writing to disk
	ContextCache      *contextcache.Cache         // When set, file reads are cached to avoid redundant disk I/O
	SnapshotManager   *snapshot.Manager           // When set, auto-snapshots after write tools
	LSPManager        *lsp.Manager                // When set, LSP diagnostics run after file edits
	TodoList          *tools.TodoList             // When set, agents can track tasks
	SharedMemory      *SharedMemory              // When set, enables inter-agent knowledge sharing
}

// RunResult summarises a completed agent run.
type RunResult struct {
	FinalText         string
	TotalSteps        int
	TotalCredits      int64
	TotalInputTokens  int
	TotalOutputTokens int
	Model             string
	FinishReason      string
	Messages          []llm.Message          // Full conversation after this run
	ProposedChanges   []tools.ProposedChange // Proposed file changes (when run with ProposalCollector)
}

// Runtime is the agent orchestration engine.
type Runtime struct {
	llmRegistry   *llm.Registry
	toolRegistry  *tools.Registry
	agentRegistry *agent.Registry
	log           zerolog.Logger
}

// New creates a new Runtime.
func New(llmReg *llm.Registry, toolReg *tools.Registry, agentReg *agent.Registry, log zerolog.Logger) *Runtime {
	return &Runtime{
		llmRegistry:   llmReg,
		toolRegistry:  toolReg,
		agentRegistry: agentReg,
		log:           log,
	}
}

// Run executes an agent to completion (up to MaxSteps). It returns the final
// result after all tool call loops are resolved.
func (r *Runtime) Run(ctx context.Context, cfg RunConfig) (*RunResult, error) {
	if cfg.AgentDef == nil {
		return nil, fmt.Errorf("agent definition is required")
	}

	// Inject ProposalCollector into context so proposal tools can accumulate changes.
	if cfg.ProposalCollector != nil {
		ctx = tools.WithProposalCollector(ctx, cfg.ProposalCollector)
	}

	state := newState(cfg)

	// Build dynamic context once for the orchestrator agent (not sub-agents).
	// Pass the user message for smart context ranking when available.
	if cfg.ProjectRoot != "" && cfg.AgentDef.ID == "base" {
		state.dynamicCtx = buildDynamicContext(cfg.ProjectRoot, cfg.UserMessage)
	}

	result := &RunResult{}

	for step := 0; step < cfg.AgentDef.MaxSteps; step++ {
		select {
		case <-ctx.Done():
			result.FinishReason = "cancelled"
			result.Messages = state.messages
			result.TotalSteps = step
			return result, ctx.Err()
		default:
		}

		if cfg.OnEvent != nil {
			cfg.OnEvent(Event{Type: EventStepStart, StepNumber: step, AgentID: cfg.AgentDef.ID, Model: cfg.AgentDef.Model})
		}

		stepResult, err := executeStep(ctx, r, state, cfg)
		if err != nil {
			r.log.Error().
				Str("agent", cfg.AgentDef.ID).
				Str("model", cfg.AgentDef.Model).
				Int("step", step).
				Err(err).
				Msg("agent step failed")
			result.FinishReason = "error"
			result.Messages = state.messages
			result.TotalSteps = step + 1
			return result, fmt.Errorf("step %d: %w", step, err)
		}

		if cfg.OnEvent != nil {
			cfg.OnEvent(Event{Type: EventStepEnd, StepNumber: step, AgentID: cfg.AgentDef.ID})
		}

		result.TotalSteps = step + 1
		result.TotalCredits += stepResult.creditsCents
		result.TotalInputTokens += stepResult.inputTokens
		result.TotalOutputTokens += stepResult.outputTokens
		if stepResult.model != "" {
			result.Model = stepResult.model
		}

		// Check token budget
		if result.TotalInputTokens > MaxRunInputTokens {
			r.log.Warn().
				Str("agent", cfg.AgentDef.ID).
				Int("total_input_tokens", result.TotalInputTokens).
				Int("budget", MaxRunInputTokens).
				Msg("agent exceeded input token budget")
			result.FinishReason = "token_budget_exceeded"
			result.Messages = state.messages
			return result, nil
		}

		// Context compaction: if the last step's input tokens indicate we're
		// approaching the context window limit, summarize older messages.
		if shouldCompact(stepResult.inputTokens, cfg.AgentDef.Model) {
			messagesBefore := len(state.messages)
			r.log.Info().
				Str("agent", cfg.AgentDef.ID).
				Int("last_step_input_tokens", stepResult.inputTokens).
				Int("messages", messagesBefore).
				Msg("triggering context compaction")

			if cfg.OnEvent != nil {
				cfg.OnEvent(Event{
					Type:    EventCompact,
					AgentID: cfg.AgentDef.ID,
					Text:    "Compacting conversation context...",
				})
			}

			compacted, compactUsg := compactMessages(ctx, r, state.messages, cfg.AgentDef.Model, cfg)
			state.messages = compacted

			// Track compaction LLM cost in the run totals.
			if compactUsg != nil {
				result.TotalInputTokens += compactUsg.InputTokens
				result.TotalOutputTokens += compactUsg.OutputTokens
				result.TotalCredits += compactUsg.CostCents
			}

			r.log.Info().
				Int("messages_before", messagesBefore).
				Int("messages_after", len(compacted)).
				Msg("context compaction complete")
		}

		// If no tool calls were made, the agent is done
		if !stepResult.hasToolCalls {
			result.FinalText = stepResult.text
			result.FinishReason = stepResult.finishReason
			result.Messages = state.messages
			return result, nil
		}
	}

	// Hit max steps
	r.log.Warn().
		Str("agent", cfg.AgentDef.ID).
		Int("max_steps", cfg.AgentDef.MaxSteps).
		Msg("agent hit max steps limit")
	result.FinishReason = "max_steps"
	result.Messages = state.messages
	return result, nil
}
