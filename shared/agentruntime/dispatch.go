package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools/editmatch"
)

// isSafeTool returns true if the tool is read-only and can safely run in parallel.
func isSafeTool(name string) bool {
	switch name {
	case "read_files", "list_directory", "glob", "find_files", "code_search", "web_search", "symbols":
		return true
	default:
		return false
	}
}

// toolResult holds the result of a single tool execution.
type toolResult struct {
	idx      int
	text     string
	isError  bool
	toolName string
	toolID   string
}

// dispatchToolCalls executes a set of tool calls and returns tool result content parts.
// Safe (read-only) tools run in parallel; unsafe tools run sequentially.
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

	// Inject LSP manager for post-edit diagnostics.
	if cfg.LSPManager != nil {
		ctx = tools.WithLSPManager(ctx, cfg.LSPManager)
	}

	// Inject todo list for task tracking.
	if cfg.TodoList != nil {
		ctx = tools.WithTodoList(ctx, cfg.TodoList)
	}

	// Partition tool calls into safe (parallelizable) and unsafe (sequential).
	// We process them in batches: consecutive safe tools run in parallel,
	// then each unsafe tool runs alone. Order is preserved.
	results := make([]toolResult, len(toolCalls))

	i := 0
	for i < len(toolCalls) {
		// Collect a batch of consecutive safe tools.
		batchStart := i
		allSafe := true
		for i < len(toolCalls) && isSafeTool(toolCalls[i].Name) {
			i++
		}

		if i > batchStart {
			// Run this batch of safe tools in parallel.
			batch := toolCalls[batchStart:i]
			if len(batch) > 1 {
				var wg sync.WaitGroup
				for j, tc := range batch {
					wg.Add(1)
					go func(idx int, tc llm.ToolCallEvent) {
						defer wg.Done()
						text, isErr := executeSingleTool(ctx, rt, tc, cfg)
						results[batchStart+idx] = toolResult{
							idx:      batchStart + idx,
							text:     text,
							isError:  isErr,
							toolName: tc.Name,
							toolID:   tc.ID,
						}
					}(j, tc)
				}
				wg.Wait()
			} else {
				// Single safe tool — no need for goroutine overhead.
				tc := batch[0]
				text, isErr := executeSingleTool(ctx, rt, tc, cfg)
				results[batchStart] = toolResult{
					idx:      batchStart,
					text:     text,
					isError:  isErr,
					toolName: tc.Name,
					toolID:   tc.ID,
				}
			}
			allSafe = true
		}

		// Process unsafe tools one at a time.
		if i < len(toolCalls) && !isSafeTool(toolCalls[i].Name) {
			tc := toolCalls[i]
			text, isErr := executeSingleTool(ctx, rt, tc, cfg)
			results[i] = toolResult{
				idx:      i,
				text:     text,
				isError:  isErr,
				toolName: tc.Name,
				toolID:   tc.ID,
			}
			i++
			allSafe = false
		}

		_ = allSafe
	}

	// Emit events and build content parts in original order.
	parts := make([]llm.ContentPart, len(toolCalls))
	for idx, r := range results {
		if cfg.OnEvent != nil {
			cfg.OnEvent(Event{
				Type:       EventToolResult,
				ToolCallID: r.toolID,
				ToolName:   r.toolName,
				Text:       r.text,
				IsError:    r.isError,
				AgentID:    cfg.AgentDef.ID,
			})
		}
		parts[idx] = llm.ContentPart{
			Type:       "tool_result",
			ToolCallID: r.toolID,
			ToolName:   r.toolName,
			Text:       r.text,
			IsError:    r.isError,
		}
	}

	return parts, nil
}

// executeSingleTool runs a single tool call and returns the result text and error flag.
func executeSingleTool(ctx context.Context, rt *Runtime, tc llm.ToolCallEvent, cfg RunConfig) (string, bool) {
	var resultText string
	var isError bool

	switch tc.Name {
	case "spawn_agents":
		result, err := handleSpawnAgents(ctx, rt, tc.ArgumentsJSON, cfg)
		if err != nil {
			rt.log.Error().Str("tool", tc.Name).Str("agent", cfg.AgentDef.ID).Err(err).Msg("sub-agent spawn failed")
			resultText = fmt.Sprintf("Error spawning agents: %v", err)
			isError = true
		} else {
			resultText = result
		}

	case "think_deeply":
		result, err := handleThinkDeeply(ctx, rt, tc.ArgumentsJSON, cfg)
		if err != nil {
			rt.log.Error().Str("tool", tc.Name).Str("agent", cfg.AgentDef.ID).Err(err).Msg("think_deeply failed")
			resultText = fmt.Sprintf("Error: %v", err)
			isError = true
		} else {
			resultText = result
		}

	case "shared_memory_write":
		if cfg.SharedMemory == nil {
			resultText = "Shared memory is not available in this context."
			isError = true
		} else {
			var args struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &args); err != nil {
				resultText = fmt.Sprintf("Error parsing args: %v", err)
				isError = true
			} else {
				cfg.SharedMemory.Write(cfg.AgentDef.ID, args.Key, args.Value)
				resultText = fmt.Sprintf("Written to shared memory: %s/%s", cfg.AgentDef.ID, args.Key)
			}
		}

	case "shared_memory_read":
		if cfg.SharedMemory == nil {
			resultText = "Shared memory is not available in this context."
			isError = true
		} else {
			summary := cfg.SharedMemory.Summary()
			if summary == "" {
				resultText = "Shared memory is empty. No other agents have written any knowledge yet."
			} else {
				resultText = summary
			}
		}

	case "apply_proposals":
		if cfg.CostMode == "plan" {
			resultText = "BLOCKED (plan mode): apply_proposals is not allowed in plan mode."
			isError = true
		} else {
			result, err := handleApplyProposals(ctx, tc.ArgumentsJSON, cfg)
			if err != nil {
				rt.log.Error().Str("tool", tc.Name).Str("agent", cfg.AgentDef.ID).Err(err).Msg("apply_proposals failed")
				resultText = fmt.Sprintf("Error applying proposals: %v", err)
				isError = true
			} else {
				resultText = result
			}
		}

	case "revert_snapshot":
		if cfg.SnapshotManager == nil {
			resultText = "Snapshot system is not available."
			isError = true
		} else {
			result, err := handleRevertSnapshot(tc.ArgumentsJSON, cfg)
			if err != nil {
				rt.log.Error().Str("tool", tc.Name).Str("agent", cfg.AgentDef.ID).Err(err).Msg("revert_snapshot failed")
				resultText = fmt.Sprintf("Error: %v", err)
				isError = true
			} else {
				resultText = result
			}
		}

	case "list_snapshots":
		if cfg.SnapshotManager == nil {
			resultText = "Snapshot system is not available."
			isError = true
		} else {
			result, err := handleListSnapshots(cfg)
			if err != nil {
				resultText = fmt.Sprintf("Error: %v", err)
				isError = true
			} else {
				resultText = result
			}
		}

	default:
		tool, ok := rt.toolRegistry.Get(tc.Name)
		if !ok {
			rt.log.Warn().Str("tool", tc.Name).Str("agent", cfg.AgentDef.ID).Msg("unknown tool requested")
			resultText = fmt.Sprintf("Unknown tool: %s", tc.Name)
			isError = true
		} else {
			result, err := tool.Execute(ctx, json.RawMessage(tc.ArgumentsJSON))
			if err != nil {
				rt.log.Error().Str("tool", tc.Name).Str("agent", cfg.AgentDef.ID).Err(err).Msg("tool execution failed")
				resultText = fmt.Sprintf("Error: %v", err)
				isError = true
			} else {
				resultText = result
			}
		}
	}

	// Auto-snapshot after write tools succeed.
	if !isError && cfg.SnapshotManager != nil && isWriteTool(tc.Name) {
		files := extractFilePaths(tc.Name, tc.ArgumentsJSON)
		if len(files) > 0 {
			cfg.SnapshotManager.Take(0, cfg.AgentDef.ID, tc.Name, files)
		}
	}

	return resultText, isError
}

// isWriteTool returns true if the tool modifies files on disk.
func isWriteTool(name string) bool {
	switch name {
	case "write_file", "str_replace", "apply_proposals", "multi_edit", "apply_patch":
		return true
	default:
		return false
	}
}

// extractFilePaths tries to extract file paths from tool arguments.
func extractFilePaths(toolName, argsJSON string) []string {
	var paths []string
	switch toolName {
	case "write_file":
		var a struct{ Path string `json:"path"` }
		if json.Unmarshal([]byte(argsJSON), &a) == nil && a.Path != "" {
			paths = append(paths, a.Path)
		}
	case "str_replace":
		var a struct{ Path string `json:"path"` }
		if json.Unmarshal([]byte(argsJSON), &a) == nil && a.Path != "" {
			paths = append(paths, a.Path)
		}
	case "multi_edit":
		var a struct {
			Edits []struct{ Path string `json:"path"` } `json:"edits"`
		}
		if json.Unmarshal([]byte(argsJSON), &a) == nil {
			seen := map[string]bool{}
			for _, e := range a.Edits {
				if e.Path != "" && !seen[e.Path] {
					paths = append(paths, e.Path)
					seen[e.Path] = true
				}
			}
		}
	}
	return paths
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
			match := editmatch.Find(content, ch.OldStr)
			if match == nil {
				summary.WriteString(fmt.Sprintf("[%d] %s: old_str not found\n", i+1, ch.Path))
				continue
			}
			newContent := content[:match.Start] + ch.NewStr + content[match.End:]
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

	provider, routedModel, err := rt.llmRegistry.Route(model)
	if err != nil {
		return "", fmt.Errorf("route thinker model: %w", err)
	}

	systemPrompt := "You are a deep thinking assistant. Analyze the following question thoroughly. Consider edge cases, trade-offs, and multiple perspectives. Think step by step."
	thinkReq := &llm.CompletionRequest{
		Model: routedModel,
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

// handleRevertSnapshot reverts project files to a previous snapshot state.
func handleRevertSnapshot(argsJSON string, cfg RunConfig) (string, error) {
	var args struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse revert_snapshot args: %w", err)
	}
	if args.SnapshotID == "" {
		return "", fmt.Errorf("snapshot_id is required")
	}

	if err := cfg.SnapshotManager.Revert(args.SnapshotID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reverted to snapshot %s", args.SnapshotID), nil
}

// handleListSnapshots returns a formatted list of recent snapshots.
func handleListSnapshots(cfg RunConfig) (string, error) {
	snaps, err := cfg.SnapshotManager.List(20)
	if err != nil {
		return "", err
	}
	if len(snaps) == 0 {
		return "No snapshots available.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Recent snapshots (%d):\n\n", len(snaps)))
	for _, s := range snaps {
		files := strings.Join(s.Files, ", ")
		if len(files) > 60 {
			files = files[:60] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %s  step:%d  agent:%s  tool:%s  %s\n    files: %s\n",
			s.ID, s.StepNum, s.AgentID, s.ToolName,
			s.Timestamp.Format("15:04:05"), files))
	}
	return sb.String(), nil
}
