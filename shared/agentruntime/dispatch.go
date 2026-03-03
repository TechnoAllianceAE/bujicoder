package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
)

// dispatchToolCalls executes a set of tool calls and returns tool result content parts.
func dispatchToolCalls(ctx context.Context, rt *Runtime, toolCalls []llm.ToolCallEvent, cfg RunConfig) ([]llm.ContentPart, error) {
	// Inject the per-request working directory so tools operate on the client's codebase.
	if cfg.ProjectRoot != "" {
		ctx = tools.WithWorkDir(ctx, cfg.ProjectRoot)
	}

	var results []llm.ContentPart

	for _, tc := range toolCalls {
		var resultText string
		var isError bool

		switch tc.Name {
		case "spawn_agents":
			// Special case: spawn sub-agents
			result, err := handleSpawnAgents(ctx, rt, tc.ArgumentsJSON, cfg)
			if err != nil {
				resultText = fmt.Sprintf("Error spawning agents: %v", err)
				isError = true
			} else {
				resultText = result
			}

		case "think_deeply":
			// Extended reasoning tool — pass through to the LLM with a reasoning prompt
			result, err := handleThinkDeeply(ctx, rt, tc.ArgumentsJSON, cfg)
			if err != nil {
				resultText = fmt.Sprintf("Error: %v", err)
				isError = true
			} else {
				resultText = result
			}

		default:
			// Dispatch to the local tool registry
			tool, ok := rt.toolRegistry.Get(tc.Name)
			if !ok {
				resultText = fmt.Sprintf("Unknown tool: %s", tc.Name)
				isError = true
			} else {
				result, err := tool.Execute(ctx, json.RawMessage(tc.ArgumentsJSON))
				if err != nil {
					resultText = fmt.Sprintf("Error: %v", err)
					isError = true
				} else {
					resultText = result
				}
			}
		}

		if cfg.OnEvent != nil {
			cfg.OnEvent(Event{
				Type:       EventToolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Text:       resultText,
				IsError:    isError,
				AgentID:    cfg.AgentDef.ID,
			})
		}

		results = append(results, llm.ContentPart{
			Type:       "tool_result",
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			Text:       resultText,
			IsError:    isError,
		})
	}

	return results, nil
}

// handleThinkDeeply sends the question to a thinker model for extended reasoning.
func handleThinkDeeply(ctx context.Context, rt *Runtime, argsJSON string, cfg RunConfig) (string, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse think_deeply args: %w", err)
	}

	// Use the thinker agent if available, otherwise use the current agent's model.
	// Apply cost mode so the thinker respects the server-resolved model.
	model := cfg.AgentDef.Model
	if thinker, ok := rt.agentRegistry.Get("thinker"); ok {
		resolved := thinker
		if cfg.CostMode != "" && cfg.ModelResolver != nil {
			resolved = thinker.WithCostMode(cfg.CostMode, cfg.ModelResolver)
		}
		model = resolved.Model
	}

	provider, _, err := rt.llmRegistry.Route(model)
	if err != nil {
		return "", fmt.Errorf("route thinker model: %w", err)
	}

	systemPrompt := "You are a deep thinking assistant. Analyze the following question thoroughly. Consider edge cases, trade-offs, and multiple perspectives. Think step by step."
	thinkReq := &llm.CompletionRequest{
		Model: model,
		Messages: []llm.Message{
			{
				Role:    "user",
				Content: []llm.ContentPart{{Type: "text", Text: args.Question}},
			},
		},
		SystemPrompt: &systemPrompt,
	}

	maxTokens := 16384
	thinkReq.MaxTokens = &maxTokens

	ch, err := provider.StreamCompletion(ctx, thinkReq)
	if err != nil {
		return "", fmt.Errorf("start thinking: %w", err)
	}

	var result string
	for ev := range ch {
		if ev.Delta != nil {
			result += ev.Delta.Text
		}
		if ev.Error != nil && !ev.Error.Retryable {
			return "", fmt.Errorf("thinking error: %s", ev.Error.Message)
		}
	}

	return result, nil
}
