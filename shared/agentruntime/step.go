package agentruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// stepResult holds the result of a single step execution.
type stepResult struct {
	text         string
	finishReason string
	hasToolCalls bool
	creditsCents int64
	inputTokens  int
	outputTokens int
	model        string
}

// executeStep runs a single LLM completion step: build request -> call LLM ->
// collect response -> if tool calls, execute tools and append results.
func executeStep(ctx context.Context, rt *Runtime, st *state, cfg RunConfig) (*stepResult, error) {
	// Compress old tool results to prevent context window bloat
	st.CompressHistory()

	// Build the completion request
	req := &llm.CompletionRequest{
		Model:    cfg.AgentDef.Model,
		Messages: st.messages,
		CostMode: string(cfg.CostMode),
		AgentID:  cfg.AgentDef.ID,
	}

	if cfg.AgentDef.SystemPrompt != "" {
		sp := cfg.AgentDef.SystemPrompt
		if cfg.AgentDef.InstructionsPrompt != "" {
			sp += "\n\n" + cfg.AgentDef.InstructionsPrompt
		}
		if st.dynamicCtx != "" {
			sp += "\n\n" + st.dynamicCtx
		}
		req.SystemPrompt = &sp
	}

	maxTokens := cfg.AgentDef.MaxTokens
	if maxTokens > 0 {
		req.MaxTokens = &maxTokens
	}

	// Add tool definitions for tools the agent has access to
	for _, toolName := range cfg.AgentDef.Tools {
		if toolName == "spawn_agents" {
			// spawn_agents is handled specially by the dispatch layer
			req.Tools = append(req.Tools, llm.ToolDefinition{
				Name:        "spawn_agents",
				Description: "Spawn one or more sub-agents to handle specific tasks in parallel",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agents": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"agent_id": map[string]any{"type": "string"},
									"task":     map[string]any{"type": "string"},
								},
								"required": []string{"agent_id", "task"},
							},
						},
					},
					"required": []string{"agents"},
				},
			})
			continue
		}
		if toolName == "think_deeply" {
			req.Tools = append(req.Tools, llm.ToolDefinition{
				Name:        "think_deeply",
				Description: "Think deeply about a problem using extended reasoning. Use this for complex analysis, planning, and decision-making.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "The question or problem to think deeply about",
						},
					},
					"required": []string{"question"},
				},
			})
			continue
		}
		if toolName == "apply_proposals" {
			req.Tools = append(req.Tools, llm.ToolDefinition{
				Name:        "apply_proposals",
				Description: "Apply a set of proposed file changes to disk. Used after the judge selects the winning implementation.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"changes": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"path":    map[string]any{"type": "string"},
									"type":    map[string]any{"type": "string", "enum": []string{"edit", "write_file"}},
									"old_str": map[string]any{"type": "string"},
									"new_str": map[string]any{"type": "string"},
									"content": map[string]any{"type": "string"},
								},
								"required": []string{"path", "type"},
							},
						},
					},
					"required": []string{"changes"},
				},
			})
			continue
		}

		tool, ok := rt.toolRegistry.Get(toolName)
		if !ok {
			continue
		}
		// Prefer the tool's own schema (e.g. from MCP), fall back to built-in.
		schema := tool.InputSchema
		if schema == nil {
			schema = toolInputSchema(tool.Name)
		}
		req.Tools = append(req.Tools, llm.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}

	// Route to the LLM provider and use the stripped model name
	// (e.g. "ollama/qooba/model" → provider=ollama, model="qooba/model").
	provider, routedModel, err := rt.llmRegistry.Route(req.Model)
	if err != nil {
		rt.log.Error().Str("model", req.Model).Str("agent", cfg.AgentDef.ID).Err(err).Msg("model routing failed")
		return nil, fmt.Errorf("route model %q: %w", req.Model, err)
	}
	req.Model = routedModel

	// Start streaming
	eventCh, err := provider.StreamCompletion(ctx, req)
	if err != nil {
		rt.log.Error().Str("model", req.Model).Str("provider", provider.Name()).Str("agent", cfg.AgentDef.ID).Err(err).Msg("LLM completion failed to start")
		return nil, fmt.Errorf("start completion: %w", err)
	}

	// Collect the response
	var textBuf strings.Builder
	var toolCalls []llm.ToolCallEvent
	var usage llm.UsageInfo
	finishReason := "stop"

	for ev := range eventCh {
		if ev.Delta != nil {
			textBuf.WriteString(ev.Delta.Text)
			if cfg.OnEvent != nil {
				cfg.OnEvent(Event{Type: EventDelta, Text: ev.Delta.Text, AgentID: cfg.AgentDef.ID})
			}
		}
		if ev.ToolCall != nil {
			toolCalls = append(toolCalls, *ev.ToolCall)
			if cfg.OnEvent != nil {
				cfg.OnEvent(Event{
					Type:       EventToolCall,
					ToolCallID: ev.ToolCall.ID,
					ToolName:   ev.ToolCall.Name,
					ArgsJSON:   ev.ToolCall.ArgumentsJSON,
					AgentID:    cfg.AgentDef.ID,
				})
			}
		}
		if ev.Complete != nil {
			usage = ev.Complete.Usage
			finishReason = ev.Complete.FinishReason
		}
		if ev.Error != nil && !ev.Error.Retryable {
			rt.log.Error().
				Str("provider", provider.Name()).
				Str("model", req.Model).
				Str("agent", cfg.AgentDef.ID).
				Str("error_code", ev.Error.Code).
				Str("error_type", "provider_error").
				Msg(ev.Error.Message)
			return nil, fmt.Errorf("provider error [%s]: %s", ev.Error.Code, ev.Error.Message)
		}
	}

	// Calculate cost from pricing service if available.
	if pricing := rt.llmRegistry.GetPricing(); pricing != nil {
		usage.CostCents = pricing.CalculateCostCents(
			req.Model, usage.InputTokens, usage.OutputTokens,
		)
	}

	result := &stepResult{
		text:         textBuf.String(),
		finishReason: finishReason,
		creditsCents: usage.CostCents,
		inputTokens:  usage.InputTokens,
		outputTokens: usage.OutputTokens,
		model:        usage.Model,
	}

	// If no tool calls, we're done
	if len(toolCalls) == 0 {
		if result.text != "" {
			st.appendAssistantText(result.text)
		}
		return result, nil
	}

	// There are tool calls — execute them
	result.hasToolCalls = true

	// Append assistant message with tool calls
	var toolCallParts []llm.ContentPart
	for _, tc := range toolCalls {
		toolCallParts = append(toolCallParts, llm.ContentPart{
			Type:          "tool_call",
			ToolCallID:    tc.ID,
			ToolName:      tc.Name,
			ArgumentsJSON: tc.ArgumentsJSON,
		})
	}
	st.appendAssistantToolCalls(result.text, toolCallParts)

	// Execute tools and collect results
	toolResults, err := dispatchToolCalls(ctx, rt, toolCalls, cfg)
	if err != nil {
		return nil, fmt.Errorf("dispatch tool calls: %w", err)
	}

	st.appendToolResults(toolResults)

	return result, nil
}

// toolInputSchema returns a basic input schema for built-in tools.
func toolInputSchema(toolName string) map[string]any {
	switch toolName {
	case "read_files":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"paths"},
		}
	case "write_file":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		}
	case "str_replace":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"old_str": map[string]any{"type": "string"},
				"new_str": map[string]any{"type": "string"},
			},
			"required": []string{"path", "old_str", "new_str"},
		}
	case "list_directory":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		}
	case "run_terminal_command":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"required": []string{"command"},
		}
	case "glob":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
			},
			"required": []string{"pattern"},
		}
	case "find_files":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
			},
			"required": []string{"pattern"},
		}
	case "code_search":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"glob":    map[string]any{"type": "string"},
			},
			"required": []string{"pattern"},
		}
	case "web_search":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		}
	case "ask_user":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
			},
			"required": []string{"question"},
		}
	case "propose_edit":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"old_str": map[string]any{"type": "string"},
				"new_str": map[string]any{"type": "string"},
			},
			"required": []string{"path", "old_str", "new_str"},
		}
	case "propose_write_file":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		}
	case "symbols":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional list of file paths to analyze. If empty, indexes the entire project.",
				},
			},
		}
	case "structured_output":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"schema": map[string]any{
					"type":        "object",
					"description": "JSON Schema to validate against",
				},
				"data": map[string]any{
					"description": "The structured data to validate",
				},
			},
			"required": []string{"schema", "data"},
		}
	default:
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
}
