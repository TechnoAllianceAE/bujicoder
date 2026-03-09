package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// dispatchToolCalls executes a set of tool calls and returns tool result content parts.
func dispatchToolCalls(ctx context.Context, rt *Runtime, toolCalls []llm.ToolCallEvent, cfg RunConfig) ([]llm.ContentPart, error) {
	// Inject the per-request working directory so tools operate on the client's codebase.
	if cfg.ProjectRoot != "" {
		ctx = tools.WithWorkDir(ctx, cfg.ProjectRoot)
	}

	// Propagate plan mode to tools so write operations are blocked.
	if cfg.CostMode == "plan" {
		ctx = tools.WithPlanMode(ctx, true)
	}

	// Inject context cache for faster repeated file reads.
	if cfg.ContextCache != nil {
		ctx = tools.WithContextCache(ctx, cfg.ContextCache)
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

		case "apply_proposals":
			if cfg.CostMode == "plan" {
				resultText = "BLOCKED (plan mode): apply_proposals is not allowed in plan mode."
				isError = true
			} else {
				result, err := handleApplyProposals(ctx, tc.ArgumentsJSON, cfg)
				if err != nil {
					resultText = fmt.Sprintf("Error applying proposals: %v", err)
					isError = true
				} else {
					resultText = result
				}
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

// handleApplyProposals applies a set of proposed file changes to disk.
func handleApplyProposals(_ context.Context, argsJSON string, cfg RunConfig) (string, error) {
	var args struct {
		Changes []struct {
			Path    string `json:"path"`
			Type    string `json:"type"`
			OldStr  string `json:"old_str"`
			NewStr  string `json:"new_str"`
			Content string `json:"content"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse apply_proposals args: %w", err)
	}

	if len(args.Changes) == 0 {
		return "No changes to apply", nil
	}

	workDir := cfg.ProjectRoot
	if workDir == "" {
		return "", fmt.Errorf("no project root configured")
	}

	var summary strings.Builder
	for i, ch := range args.Changes {
		absPath, err := tools.SafePath(workDir, ch.Path)
		if err != nil {
			summary.WriteString(fmt.Sprintf("[%d] %s: error: %v\n", i+1, ch.Path, err))
			continue
		}

		switch ch.Type {
		case "edit":
			data, err := os.ReadFile(absPath)
			if err != nil {
				summary.WriteString(fmt.Sprintf("[%d] %s: read error: %v\n", i+1, ch.Path, err))
				continue
			}
			content := string(data)
			if !strings.Contains(content, ch.OldStr) {
				summary.WriteString(fmt.Sprintf("[%d] %s: old_str not found\n", i+1, ch.Path))
				continue
			}
			newContent := strings.Replace(content, ch.OldStr, ch.NewStr, 1)
			if err := os.WriteFile(absPath, []byte(newContent), 0o644); err != nil {
				summary.WriteString(fmt.Sprintf("[%d] %s: write error: %v\n", i+1, ch.Path, err))
				continue
			}
			summary.WriteString(fmt.Sprintf("[%d] %s: edit applied\n", i+1, ch.Path))

		case "write_file":
			if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
				summary.WriteString(fmt.Sprintf("[%d] %s: mkdir error: %v\n", i+1, ch.Path, err))
				continue
			}
			if err := os.WriteFile(absPath, []byte(ch.Content), 0o644); err != nil {
				summary.WriteString(fmt.Sprintf("[%d] %s: write error: %v\n", i+1, ch.Path, err))
				continue
			}
			summary.WriteString(fmt.Sprintf("[%d] %s: file written (%d bytes)\n", i+1, ch.Path, len(ch.Content)))

		default:
			summary.WriteString(fmt.Sprintf("[%d] %s: unknown type %q\n", i+1, ch.Path, ch.Type))
		}
	}

	return summary.String(), nil
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
