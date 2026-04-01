package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// spawnRequest represents the parsed arguments for spawn_agents.
type spawnRequest struct {
	Agents []spawnAgentSpec `json:"agents"`
}

type spawnAgentSpec struct {
	AgentID string `json:"agent_id"`
	Task    string `json:"task"`
}

// handleSpawnAgents spawns sub-agents concurrently and collects their results.
func handleSpawnAgents(ctx context.Context, rt *Runtime, argsJSON string, parentCfg RunConfig) (string, error) {
	var req spawnRequest
	if err := json.Unmarshal([]byte(argsJSON), &req); err != nil {
		return "", fmt.Errorf("parse spawn_agents args: %w", err)
	}

	if len(req.Agents) == 0 {
		return "No agents specified to spawn", nil
	}

	// Validate that all requested agents exist and are spawnable
	spawnableSet := make(map[string]bool)
	for _, id := range parentCfg.AgentDef.SpawnableAgents {
		spawnableSet[id] = true
	}

	for _, spec := range req.Agents {
		if !spawnableSet[spec.AgentID] {
			return "", fmt.Errorf("agent %q is not in the spawnable list for %s", spec.AgentID, parentCfg.AgentDef.ID)
		}
		if _, ok := rt.agentRegistry.Get(spec.AgentID); !ok {
			return "", fmt.Errorf("agent %q not found in registry", spec.AgentID)
		}
	}

	type spawnResult struct {
		agentID string
		result  *RunResult
		err     error
	}

	results := make([]spawnResult, len(req.Agents))
	var wg sync.WaitGroup

	for i, spec := range req.Agents {
		wg.Add(1)
		go func(idx int, s spawnAgentSpec) {
			defer wg.Done()

			// Emit "starting" status
			if parentCfg.OnEvent != nil {
				task := s.Task
				if len(task) > 100 {
					task = task[:100] + "..."
				}
				parentCfg.OnEvent(Event{
					Type:    EventStatus,
					AgentID: s.AgentID,
					Text:    fmt.Sprintf("Starting %s: %s", s.AgentID, task),
				})
			}

			agentDef, _ := rt.agentRegistry.Get(s.AgentID)

			// Apply cost mode so sub-agents use the server-resolved model.
			if parentCfg.CostMode != "" && parentCfg.ModelResolver != nil {
				agentDef = agentDef.WithCostMode(parentCfg.CostMode, parentCfg.ModelResolver)
			}

			// Build ancestor chain
			ancestors := make([]string, len(parentCfg.AncestorIDs))
			copy(ancestors, parentCfg.AncestorIDs)

			// If the agent has proposal tools, give it a ProposalCollector.
			var collector *tools.ProposalCollector
			if agentHasProposalTools(agentDef) {
				collector = tools.NewProposalCollector()
			}

			childCfg := RunConfig{
				AgentDef:          agentDef,
				UserMessage:       s.Task,
				AncestorIDs:       ancestors,
				ProjectRoot:       parentCfg.ProjectRoot,
				CostMode:          parentCfg.CostMode,
				ModelResolver:     parentCfg.ModelResolver,
				ProposalCollector: collector,
				SharedMemory:      parentCfg.SharedMemory, // Share memory across agents
				OnEvent: func(ev Event) {
					if parentCfg.OnEvent == nil {
						return
					}
					// Sub-agent events are forwarded with their AgentID.
					// The UI is responsible for handling interleaved deltas and formatting.
					ev.AgentID = s.AgentID
					parentCfg.OnEvent(ev)
				},
			}

			result, err := rt.Run(ctx, childCfg)

			// Extract proposals from collector into the result.
			if err == nil && result != nil && collector != nil {
				result.ProposedChanges = collector.Changes()
			}
			results[idx] = spawnResult{
				agentID: s.AgentID,
				result:  result,
				err:     err,
			}

			// Emit "completed" status
			if parentCfg.OnEvent != nil {
				statusText := fmt.Sprintf("Completed %s", s.AgentID)
				if result != nil {
					statusText += fmt.Sprintf(" (%d steps)", result.TotalSteps)
				}
				if err != nil {
					statusText += " (error)"
				}
				parentCfg.OnEvent(Event{
					Type:    EventStatus,
					AgentID: s.AgentID,
					Text:    statusText,
				})
			}
		}(i, spec)
	}

	wg.Wait()

	// Format results
	var output strings.Builder
	for _, r := range results {
		output.WriteString(fmt.Sprintf("=== Agent: %s ===\n", r.agentID))
		if r.err != nil {
			output.WriteString(fmt.Sprintf("Error: %v\n", r.err))
		} else if r.result != nil {
			output.WriteString(r.result.FinalText)
			if len(r.result.ProposedChanges) > 0 {
				output.WriteString("\n--- Proposed Changes ---\n")
				for _, ch := range r.result.ProposedChanges {
					output.WriteString(ch.DiffText)
					output.WriteString("\n")
				}
			}
			output.WriteString(fmt.Sprintf("\n[Steps: %d, Finish: %s]\n", r.result.TotalSteps, r.result.FinishReason))
		}
		output.WriteString("\n")
	}

	return output.String(), nil
}

// agentHasProposalTools returns true if the agent definition includes
// propose_edit or propose_write_file in its tools list.
func agentHasProposalTools(def *agent.Definition) bool {
	for _, t := range def.Tools {
		if t == "propose_edit" || t == "propose_write_file" {
			return true
		}
	}
	return false
}
